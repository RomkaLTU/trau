package pipeline

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/attachfile"
	"github.com/RomkaLTU/trau/internal/prompts"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// attachTracker adds both attachment capabilities to the shared fakeTracker so
// ticketContext can materialize a ticket's files.
type attachTracker struct {
	*fakeTracker
	detail tracker.IssueDetail
	blobs  map[int64]string
}

func (t attachTracker) IssueDetail(context.Context, string) (tracker.IssueDetail, error) {
	return t.detail, nil
}

func (t attachTracker) AttachmentBytes(_ context.Context, id int64) ([]byte, error) {
	body, ok := t.blobs[id]
	if !ok {
		return nil, errors.New("hub unreachable")
	}
	return []byte(body), nil
}

// A run on a ticket with an embedded screenshot builds a TicketContext naming a
// local image path instead of the tracker URL, plus an Attachments section — and
// the build instruction carries both through to the agent.
func TestTicketContextMaterializesAttachments(t *testing.T) {
	id := "COD-1041-materialize"
	t.Cleanup(func() { attachfile.Remove(id) })

	p := &Pipeline{Tracker: attachTracker{
		fakeTracker: &fakeTracker{},
		detail: tracker.IssueDetail{
			Title:       "Broken toolbar",
			Description: "It looks wrong:\n\n![screenshot](https://uploads.linear.app/abc/shot.png)",
			Attachments: []tracker.AttachmentRef{
				{ID: 1, Filename: "shot.png", MimeType: "image/png", IsImage: true, SourceURL: "https://uploads.linear.app/abc/shot.png"},
				{ID: 2, Filename: "run.log", MimeType: "text/plain", SourceURL: "https://uploads.linear.app/abc/run.log"},
			},
		},
		blobs: map[int64]string{1: "PNGDATA", 2: "stack trace"},
	}}

	got := p.ticketContext(context.Background(), id)
	imagePath := attachfile.Dir(id) + "/shot.png"
	logPath := attachfile.Dir(id) + "/run.log"

	mustContain(t, "ticketContext", got,
		"![screenshot]("+imagePath+")",
		"--- Attachments ---",
		imagePath,
		logPath,
		"image/png",
		"text/plain",
		"read them",
	)
	mustNotContain(t, "ticketContext", got, "https://uploads.linear.app/abc/shot.png")

	for _, path := range []string{imagePath, logPath} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("materialized file missing: %v", err)
		}
	}

	build := buildInstruction(prompts.Renderer{}, id, "feature/x", skillsPrompt(prompts.Renderer{}, nil, nil), "", got)
	mustContain(t, "buildInstruction", build, imagePath, "--- Attachments ---")
}

// A file the hub cannot deliver leaves the run intact: the description keeps its
// original URL and the context notes the file as unavailable.
func TestTicketContextUnavailableAttachmentDegrades(t *testing.T) {
	id := "COD-1041-unavailable"
	t.Cleanup(func() { attachfile.Remove(id) })

	p := &Pipeline{Tracker: attachTracker{
		fakeTracker: &fakeTracker{},
		detail: tracker.IssueDetail{
			Title:       "Broken toolbar",
			Description: "![screenshot](https://uploads.linear.app/abc/shot.png)",
			Attachments: []tracker.AttachmentRef{
				{ID: 1, Filename: "shot.png", MimeType: "image/png", IsImage: true, SourceURL: "https://uploads.linear.app/abc/shot.png"},
			},
		},
	}}

	got := p.ticketContext(context.Background(), id)
	mustContain(t, "ticketContext", got,
		"Broken toolbar",
		"shot.png unavailable:",
		"https://uploads.linear.app/abc/shot.png",
	)
}

// Nothing is materialized inside the repository working tree — the copies live
// under the ticket's /tmp directory, which the build sweep clears.
func TestAttachmentsStayOutOfTheRepo(t *testing.T) {
	id := "COD-1041-tmp"
	if dir := attachfile.Dir(id); !strings.HasPrefix(dir, "/tmp/") {
		t.Fatalf("attachment dir = %q, want a /tmp path so nothing lands in the target repo", dir)
	}
}
