// Package attachfile materializes an issue's images and files as local copies an
// agent can read, and repoints the references embedded in issue text at them. The
// copies live under /tmp beside the other agent-interface artifacts, never inside
// the target repository's working tree.
package attachfile

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const dirPrefix = "/tmp/trau-attachments-"

// readNote is the single instruction that closes the file list. It stays
// framework-agnostic: what the files are, and why an image is worth opening.
const readNote = "These are local copies of the ticket's files. Images may show UI states or error screenshots that matter for this task — read them.\n"

// hubRef matches the hub's own attachment URL, the form an uploaded image carries
// in an issue body or a grilling answer. Tracker-hosted files are matched by their
// recorded source URL instead.
var hubRef = regexp.MustCompile(`(?:https?://[^/\s)"'<>]+)?(?:/api/v1)?/repos/[^/\s)"'<>]+/attachments/(\d+)`)

// Ref is one of an issue's attachments as the issue store knows it — enough to
// fetch the bytes and describe the file to an agent.
type Ref struct {
	ID        int64
	Filename  string
	MimeType  string
	Size      int64
	IsImage   bool
	SourceURL string
}

// File is a Ref after materialization: Path names the local copy, or Err says why
// there is none.
type File struct {
	Ref
	Path string
	Err  error
}

// Fetcher reads an attachment's bytes. The hub owns the attachment cache, so a
// child process satisfies this over HTTP rather than opening the store itself.
type Fetcher func(ctx context.Context, id int64) ([]byte, error)

// Dir is the directory a ticket's attachments materialize into.
func Dir(ticket string) string { return dirPrefix + ticket }

// Remove drops a ticket's materialized files.
func Remove(ticket string) { _ = os.RemoveAll(Dir(ticket)) }

// Materialize writes each ref's bytes into the ticket's directory. A ref that will
// not fetch or write comes back carrying its error instead of a path: a missing
// screenshot degrades the prompt, it never fails the run. Size and — for a row the
// hub has not cached yet, which knows no type until it does — the mime type are
// taken from the bytes actually read, so a freshly synced screenshot is described
// as an image on the first run rather than the second.
func Materialize(ctx context.Context, ticket string, refs []Ref, fetch Fetcher) []File {
	if len(refs) == 0 {
		return nil
	}
	out := make([]File, 0, len(refs))
	dir := Dir(ticket)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		for _, ref := range refs {
			out = append(out, File{Ref: ref, Err: err})
		}
		return out
	}
	taken := map[string]bool{}
	for _, ref := range refs {
		body, err := fetch(ctx, ref.ID)
		if err != nil {
			out = append(out, File{Ref: ref, Err: err})
			continue
		}
		path := filepath.Join(dir, unique(taken, safeName(ref)))
		if err := os.WriteFile(path, body, 0o644); err != nil {
			out = append(out, File{Ref: ref, Err: err})
			continue
		}
		ref.Size = int64(len(body))
		if strings.TrimSpace(ref.MimeType) == "" {
			ref.MimeType = http.DetectContentType(body)
			ref.IsImage = isImage(ref.MimeType)
		}
		out = append(out, File{Ref: ref, Path: path})
	}
	return out
}

// Rewrite repoints the references in body — tracker URLs and the hub's own
// attachment URLs alike — at the materialized copies, so a Markdown image the
// agent reads names a path it can open. A file that failed to materialize keeps
// its original URL; the list says why it is unavailable.
func Rewrite(body string, files []File) string {
	if body == "" {
		return body
	}
	paths := make(map[string]string, len(files))
	for _, f := range files {
		if f.Path == "" {
			continue
		}
		paths[strconv.FormatInt(f.ID, 10)] = f.Path
		if f.SourceURL != "" {
			body = strings.ReplaceAll(body, f.SourceURL, f.Path)
		}
	}
	return hubRef.ReplaceAllStringFunc(body, func(ref string) string {
		if path, ok := paths[hubRef.FindStringSubmatch(ref)[1]]; ok {
			return path
		}
		return ref
	})
}

// Section renders the prompt block listing the ticket's files, or "" when there
// are none to describe.
func Section(files []File) string {
	var b strings.Builder
	for _, f := range files {
		if f.Err != nil {
			fmt.Fprintf(&b, "attachment %s unavailable: %v\n", f.name(), f.Err)
			continue
		}
		fmt.Fprintf(&b, "%s — %s (%s, %s)\n", f.Path, f.name(), f.mimeType(), size(f.Size))
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n--- Attachments ---\n" + b.String() + readNote
}

// IDsIn returns the hub attachment ids referenced in body, in first-seen order.
// It is how an image pasted mid-interview is discovered: the upload lands as a hub
// URL in the answer text rather than on the issue.
func IDsIn(body string) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, m := range hubRef.FindAllStringSubmatch(body, -1) {
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func (f File) name() string {
	if n := strings.TrimSpace(f.Filename); n != "" {
		return n
	}
	return "attachment-" + strconv.FormatInt(f.ID, 10)
}

// imageTypes are the pictures worth telling an agent to open. It mirrors the set
// the hub renders inline: SVG is a scriptable document, not a screenshot.
var imageTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

func isImage(mime string) bool {
	mime, _, _ = strings.Cut(mime, ";")
	return imageTypes[strings.ToLower(strings.TrimSpace(mime))]
}

func (f File) mimeType() string {
	if m := strings.TrimSpace(f.MimeType); m != "" {
		return m
	}
	return "unknown type"
}

// safeName reduces a tracker-supplied filename to one path segment: a slash or a
// traversal in it must not place the file outside the ticket's directory.
func safeName(ref Ref) string {
	name := filepath.Base(filepath.Clean("/" + strings.TrimSpace(ref.Filename)))
	if name == string(filepath.Separator) {
		return "attachment-" + strconv.FormatInt(ref.ID, 10)
	}
	return name
}

// unique keeps two attachments sharing a filename from overwriting each other. The
// suffix it appends is searched rather than counted, because a ticket can carry a
// file literally named shot-2.png alongside two called shot.png.
func unique(taken map[string]bool, name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 2; taken[name]; n++ {
		name = fmt.Sprintf("%s-%d%s", stem, n, ext)
	}
	taken[name] = true
	return name
}

func size(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1fKB", float64(n)/1024)
}
