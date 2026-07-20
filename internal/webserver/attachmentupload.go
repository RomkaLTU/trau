package webserver

import (
	"bufio"
	"errors"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
)

// uploadMaxBytes caps a pasted, dropped, or picked image. A screenshot sits well
// under it; anything larger is refused rather than stored.
const uploadMaxBytes = 10 << 20

// handleUploadAttachment stores an image a user put into a trau-native editor —
// POST /repos/{repo}/attachments, multipart with a `file` field. The bytes land
// in the content-addressed store at once (source=upload, state=cached) and the
// row stays unbound until the issue whose markdown references it is saved, so an
// upload the user abandons is left for the retention sweep. Only image types are
// accepted and only up to uploadMaxBytes; a rejected upload stores nothing.
func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, uploadMaxBytes+(1<<20))
	file, header, err := r.FormFile("file")
	if err != nil {
		if tooLarge(err) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "image exceeds 10 MB"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected a file field"})
		return
	}
	defer func() { _ = file.Close() }()

	buffered := bufio.NewReader(file)
	prefix, _ := buffered.Peek(512)
	mimeType, ok := detectUploadMime(header, prefix)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "only image uploads are supported"})
		return
	}

	sha, size, err := s.stores.Attachments().Blobs().Put(buffered, uploadMaxBytes)
	if err != nil {
		if errors.Is(err, hubstore.ErrAttachmentTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "image exceeds 10 MB"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store upload: " + err.Error()})
		return
	}

	att, err := s.stores.Attachments().Create(hubstore.Attachment{
		Repo:      repo.Root,
		Source:    hubstore.AttachmentSourceUpload,
		Filename:  uploadFilename(header),
		MimeType:  mimeType,
		SizeBytes: size,
		SHA256:    sha,
		State:     hubstore.AttachmentCached,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "register upload: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, attachmentView(repo, att))
}

// detectUploadMime settles an upload's type from its bytes, not the header a
// client can spoof: the raster types are recognised by their signature, and SVG —
// which has none and can carry script — is admitted only when the client labels
// it one and the bytes read as XML, then served as a download by the same rule
// the fetch path uses.
func detectUploadMime(header *multipart.FileHeader, prefix []byte) (string, bool) {
	sniff, _, _ := strings.Cut(http.DetectContentType(prefix), ";")
	sniff = strings.ToLower(strings.TrimSpace(sniff))
	if hubstore.AttachmentIsImage(sniff) {
		return sniff, true
	}
	if uploadIsSVG(header, prefix) {
		return "image/svg+xml", true
	}
	return "", false
}

func uploadIsSVG(header *multipart.FileHeader, prefix []byte) bool {
	declared, _, _ := strings.Cut(header.Header.Get("Content-Type"), ";")
	declared = strings.ToLower(strings.TrimSpace(declared))
	if declared != "image/svg+xml" && !strings.EqualFold(filepath.Ext(header.Filename), ".svg") {
		return false
	}
	const bom = "\ufeff"
	head := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(string(prefix), bom)))
	return strings.HasPrefix(head, "<?xml") || strings.Contains(head, "<svg")
}

func uploadFilename(header *multipart.FileHeader) string {
	name := filepath.Base(strings.TrimSpace(header.Filename))
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

func tooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

// uploadedAttachmentRef matches the canonical serve path an uploaded image
// renders as. Anchoring on the repos route keeps a stray URL that merely contains
// a number from reading as an attachment id.
var uploadedAttachmentRef = regexp.MustCompile(`/repos/[^/\s)"'<>]+/attachments/(\d+)`)

// scanUploadedAttachmentIDs pulls the ids of the hub uploads a body embeds. Ids
// belonging to another repo never take effect: binding is repo-scoped.
func scanUploadedAttachmentIDs(description string) []int64 {
	matches := uploadedAttachmentRef.FindAllStringSubmatch(description, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[int64]bool{}
	ids := make([]int64, 0, len(matches))
	for _, m := range matches {
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// bindUploadedAttachments binds the uploads an issue's markdown references to the
// issue so they follow its lifecycle instead of lingering unowned. Failures are
// logged, not fatal: the issue content already landed and an unbound upload is
// swept by the retention pass.
func (s *Server) bindUploadedAttachments(root, identifier, description string) {
	ids := scanUploadedAttachmentIDs(description)
	if len(ids) == 0 {
		return
	}
	if err := s.stores.Attachments().BindToIssue(root, ids, identifier); err != nil {
		logger.Verbosef("attachments %s %s: bind uploads: %v", root, identifier, err)
	}
}
