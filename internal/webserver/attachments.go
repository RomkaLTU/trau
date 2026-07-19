package webserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
)

// attachmentMaxFetchBytes caps a lazily downloaded file. A tracker holding
// something larger is refused rather than allowed to fill the cache.
const attachmentMaxFetchBytes = 50 << 20

// attachmentClient fetches tracker-hosted bytes. The timeout bounds the whole
// download, so it is generous enough for a file at the size cap on a slow link
// rather than the API clients' 30s.
var attachmentClient = &http.Client{Timeout: 2 * time.Minute}

// AttachmentView is one attachment on the wire: its stored record plus the two
// things a client cannot derive — whether trau will render it inline, and where
// to fetch its bytes.
type AttachmentView struct {
	hubstore.Attachment
	IsImage bool   `json:"is_image"`
	URL     string `json:"url"`
}

// handleIssueAttachments lists an issue's attachments — GET
// /repos/{repo}/issues/{id}/attachments, reached through the issue-action
// wildcard. It reports what is known without fetching anything: a pending row
// stays pending until something asks for its bytes.
func (s *Server) handleIssueAttachments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	rows, err := s.stores.Attachments().ForIssue(repo.Root, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list attachments: " + err.Error()})
		return
	}
	out := make([]AttachmentView, 0, len(rows))
	for _, at := range rows {
		out = append(out, attachmentView(repo, at))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAttachment serves an attachment's bytes — GET
// /repos/{repo}/attachments/{id}. A row whose bytes are not cached is downloaded
// now with the repo's tracker credentials and stored before it is served, so the
// first view pays for the fetch and every later one reads disk. That makes a
// failed row self-healing: it retries on the next request rather than staying
// broken until a re-sync. A fetch that fails answers 502 carrying the reason the
// list endpoint also shows.
func (s *Server) handleAttachment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown attachment"})
		return
	}
	att, found, err := s.stores.Attachments().Get(repo.Root, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read attachment: " + err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown attachment"})
		return
	}
	if att.State != hubstore.AttachmentCached {
		if att.SourceURL == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "attachment has no stored bytes"})
			return
		}
		if att, err = s.fetchAttachment(r.Context(), repo, att); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "fetch attachment: " + err.Error()})
			return
		}
	}
	s.serveAttachmentBytes(w, r, att)
}

// serveAttachmentBytes writes a cached attachment. The response is immutable —
// the URL addresses a row whose bytes are pinned by their digest — so it carries
// a year-long cache lifetime and the digest as its ETag. Only the raster image
// types render inline; everything else, SVG included, downloads.
func (s *Server) serveAttachmentBytes(w http.ResponseWriter, r *http.Request, att hubstore.Attachment) {
	f, err := s.stores.Attachments().Blobs().Open(att.SHA256)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "open attachment: " + err.Error()})
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stat attachment: " + err.Error()})
		return
	}

	name := attachmentFilename(att)
	disposition := "attachment"
	if hubstore.AttachmentIsImage(att.MimeType) {
		disposition = "inline"
	}
	if att.MimeType != "" {
		w.Header().Set("Content-Type", att.MimeType)
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+att.SHA256+`"`)
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// fetchAttachment downloads and caches an attachment's bytes, collapsing
// concurrent first views of the same row onto one download: the drawer rendering
// an image three times must not hit the tracker three times.
func (s *Server) fetchAttachment(ctx context.Context, repo registry.Repo, att hubstore.Attachment) (hubstore.Attachment, error) {
	key := repo.Root + "\x00" + strconv.FormatInt(att.ID, 10)
	cached, err, _ := s.attachFetch.Do(key, func() (any, error) {
		return s.downloadAttachment(ctx, repo, att)
	})
	if err != nil {
		return hubstore.Attachment{}, err
	}
	return cached.(hubstore.Attachment), nil
}

// downloadAttachment pulls the source, stores the bytes, and settles the row —
// cached with its digest, or failed with the reason a later retry replaces.
func (s *Server) downloadAttachment(ctx context.Context, repo registry.Repo, att hubstore.Attachment) (hubstore.Attachment, error) {
	atts := s.stores.Attachments()
	body, mimeType, err := s.openAttachmentSource(ctx, repo, att)
	if err != nil {
		return hubstore.Attachment{}, failAttachment(atts, att.ID, err.Error())
	}
	defer func() { _ = body.Close() }()

	sha, size, err := atts.Blobs().Put(body, attachmentMaxFetchBytes)
	if err != nil {
		reason := err.Error()
		if errors.Is(err, hubstore.ErrAttachmentTooLarge) {
			reason = "too large"
		}
		return hubstore.Attachment{}, failAttachment(atts, att.ID, reason)
	}
	if err := atts.MarkCached(att.ID, sha, size, mimeType); err != nil {
		return hubstore.Attachment{}, err
	}

	att.SHA256, att.SizeBytes, att.State, att.Error = sha, size, hubstore.AttachmentCached, ""
	if mimeType != "" {
		att.MimeType = mimeType
	}
	return att, nil
}

// openAttachmentSource starts the download, authenticated for the source that
// holds the file. The caller closes the body.
func (s *Server) openAttachmentSource(ctx context.Context, repo registry.Repo, att hubstore.Attachment) (io.ReadCloser, string, error) {
	parsed, err := url.Parse(att.SourceURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, "", errors.New("attachment source is not an http(s) URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, att.SourceURL, nil)
	if err != nil {
		return nil, "", err
	}
	if err := s.authorizeAttachment(repo, att.Source, req); err != nil {
		return nil, "", err
	}
	res, err := attachmentClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	if res.StatusCode != http.StatusOK {
		_ = res.Body.Close()
		return nil, "", fmt.Errorf("source responded %s", res.Status)
	}
	contentType, _, _ := strings.Cut(res.Header.Get("Content-Type"), ";")
	return res.Body, strings.TrimSpace(contentType), nil
}

// authorizeAttachment puts the repo's tracker credentials on the request — the
// same ones sync reads the issue with, since the file sits behind the same auth.
// An external file is public and carries none.
func (s *Server) authorizeAttachment(repo registry.Repo, source string, req *http.Request) error {
	if source != hubstore.AttachmentSourceLinear && source != hubstore.AttachmentSourceJira {
		return nil
	}
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		return err
	}
	switch source {
	case hubstore.AttachmentSourceLinear:
		if cfg.LinearAPIKey == "" {
			return errors.New("no Linear credentials configured for this repo")
		}
		req.Header.Set("Authorization", cfg.LinearAPIKey)
	case hubstore.AttachmentSourceJira:
		auth := jiraapi.BasicAuth(cfg.JiraEmail, cfg.JiraAPIToken)
		if auth == "" {
			return errors.New("no Jira credentials configured for this repo")
		}
		req.Header.Set("Authorization", auth)
	}
	return nil
}

// dropRepoAttachments clears a repo's attachments when it is unregistered, so no
// row or cached file outlives the repo that owned it.
func (s *Server) dropRepoAttachments(root string) {
	if err := s.stores.Attachments().DeleteForRepo(root); err != nil {
		logger.Verbosef("drop attachments for %s: %v", root, err)
	}
}

// failAttachment records why a fetch produced no bytes and returns that reason as
// the request's error, so the 502 and the list endpoint tell the same story.
func failAttachment(atts *hubstore.Attachments, id int64, reason string) error {
	if err := atts.MarkFailed(id, reason); err != nil {
		logger.Verbosef("mark attachment %d failed: %v", id, err)
	}
	return errors.New(reason)
}

func attachmentView(repo registry.Repo, att hubstore.Attachment) AttachmentView {
	return AttachmentView{
		Attachment: att,
		IsImage:    hubstore.AttachmentIsImage(att.MimeType),
		URL:        fmt.Sprintf("%s/repos/%s/attachments/%d", APIPrefix, url.PathEscape(repo.Name), att.ID),
	}
}

func attachmentFilename(att hubstore.Attachment) string {
	if name := strings.TrimSpace(att.Filename); name != "" {
		return name
	}
	return "attachment-" + strconv.FormatInt(att.ID, 10)
}
