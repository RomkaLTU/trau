package tracker

import (
	"net/url"
	"path"
	"regexp"
	"strings"
)

// Attachment sources, mirroring the hub store's values: where a discovered file
// lives, which decides the credentials a later fetch presents for it.
const (
	AttachmentLinear   = "linear"
	AttachmentJira     = "jira"
	AttachmentExternal = "external"
)

// linearUploadHost is where Linear serves the files pasted into an issue.
const linearUploadHost = "uploads.linear.app"

// Attachment is one file an issue references: where its bytes live and whatever
// is known about them without downloading anything. MimeType and Size are set
// only when the tracker's own file list reported them; a file found by reading
// markdown carries just a URL and the name derived from it.
type Attachment struct {
	URL      string
	Filename string
	MimeType string
	Size     int64
	Source   string
}

// AttachmentScanner finds the files an issue's markdown references and decides
// which credentials reach each one. JiraHost is the repo's Jira site host, empty
// for a repo that is not Jira-backed — the zero scanner still recognises Linear
// uploads and classifies everything else as external.
type AttachmentScanner struct {
	JiraHost string
}

// NewAttachmentScanner returns a scanner that recognises baseURL's host as the
// repo's Jira site.
func NewAttachmentScanner(baseURL string) AttachmentScanner {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return AttachmentScanner{}
	}
	return AttachmentScanner{JiraHost: strings.ToLower(parsed.Host)}
}

// markdownImage matches an `![alt](url)` reference, tolerating the angle-bracket
// and quoted-title forms CommonMark allows.
var markdownImage = regexp.MustCompile(`!\[([^\]]*)\]\(\s*<?([^)>\s]+)>?(?:\s+"[^"]*")?\s*\)`)

// bareURL matches an http(s) URL written without markdown syntax. The character
// class stops at the delimiters that surround a URL rather than belong to it.
var bareURL = regexp.MustCompile(`https?://[^\s<>()\[\]"']+`)

var imageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".svg": true, ".bmp": true, ".avif": true,
}

// Scan returns the distinct files the markdown bodies reference, in first-seen
// order: every `![](url)` image plus any bare http(s) URL naming an image file.
// A URL seen twice — the same screenshot in a description and in a comment —
// yields one attachment.
func (s AttachmentScanner) Scan(bodies ...string) []Attachment {
	var out []Attachment
	seen := map[string]bool{}
	add := func(raw, alt string) {
		att, ok := s.ref(raw, alt)
		if !ok || seen[att.URL] {
			return
		}
		seen[att.URL] = true
		out = append(out, att)
	}
	for _, body := range bodies {
		for _, m := range markdownImage.FindAllStringSubmatch(body, -1) {
			add(m[2], m[1])
		}
		for _, raw := range bareURL.FindAllString(body, -1) {
			raw = strings.TrimRight(raw, ".,;:!?")
			if isImageURL(raw) {
				add(raw, "")
			}
		}
	}
	return out
}

// Classify reports which credentials reach rawURL: the repo's Linear or Jira
// site, or none for a file hosted anywhere else.
func (s AttachmentScanner) Classify(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return AttachmentExternal
	}
	return s.classify(parsed)
}

func (s AttachmentScanner) classify(u *url.URL) string {
	host := strings.ToLower(u.Host)
	switch {
	case host == linearUploadHost:
		return AttachmentLinear
	case s.JiraHost != "" && host == s.JiraHost:
		return AttachmentJira
	default:
		return AttachmentExternal
	}
}

// ref turns a discovered reference into an attachment, rejecting anything that
// is not an absolute http(s) URL — a relative path, a data: URI, or the trau
// attachment route an uploaded image renders as, which is already a stored row.
func (s AttachmentScanner) ref(raw, alt string) (Attachment, bool) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return Attachment{}, false
	}
	return Attachment{
		URL:      raw,
		Filename: attachmentName(parsed, alt),
		Source:   s.classify(parsed),
	}, true
}

// mergeAttachments appends the scanned refs the tracker's own file list did not
// already cover, so a file the API described keeps the filename, type and size it
// reported rather than the guess derived from its URL.
func mergeAttachments(listed, found []Attachment) []Attachment {
	out := listed
	known := make(map[string]bool, len(listed))
	for _, att := range listed {
		known[att.URL] = true
	}
	for _, att := range found {
		if !known[att.URL] {
			out = append(out, att)
		}
	}
	return out
}

// attachmentName derives a display name from the URL's last path segment,
// falling back to the image's alt text when the URL carries no usable one.
func attachmentName(u *url.URL, alt string) string {
	name := path.Base(u.Path)
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	if name == "." || name == "/" {
		name = ""
	}
	if name == "" {
		return strings.TrimSpace(alt)
	}
	return name
}

func isImageURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return imageExtensions[strings.ToLower(path.Ext(parsed.Path))]
}
