package webserver

import (
	"errors"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/attachfile"
)

// A grilling prompt points the agent at the local copy of the issue's screenshot
// and lists the file, so it can describe what the image shows while interviewing.
func TestGrillIssuePromptReferencesMaterializedFiles(t *testing.T) {
	files := []attachfile.File{
		{Ref: attachfile.Ref{ID: 1, Filename: "shot.png", MimeType: "image/png", Size: 2048, IsImage: true, SourceURL: "https://uploads.linear.app/abc/shot.png"}, Path: "/tmp/trau-attachments-COD-1/shot.png"},
	}
	body := "The toolbar breaks:\n\n![shot](https://uploads.linear.app/abc/shot.png)"

	for name, got := range map[string]string{
		"grillIssuePrompt":    grillIssuePrompt("COD-1", "Broken toolbar", body, files),
		"grillPregrillPrompt": grillPregrillPrompt("COD-1", "Broken toolbar", body, files),
	} {
		for _, want := range []string{
			"![shot](/tmp/trau-attachments-COD-1/shot.png)",
			"--- Attachments ---",
			"image/png",
			"read them",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: missing %q in:\n%s", name, want, got)
			}
		}
		if strings.Contains(got, "https://uploads.linear.app/abc/shot.png") {
			t.Errorf("%s: kept the unreachable tracker URL", name)
		}
	}
}

// An issue with no files renders exactly as it did before attachments existed.
func TestGrillIssuePromptWithoutAttachments(t *testing.T) {
	got := grillIssuePrompt("COD-1", "Broken toolbar", "Fix the toolbar.", nil)
	if strings.Contains(got, "--- Attachments ---") {
		t.Errorf("empty attachment list should render no section:\n%s", got)
	}
	if !strings.Contains(got, "Fix the toolbar.\n\n") {
		t.Errorf("description spacing changed:\n%s", got)
	}
	if empty := grillIssuePrompt("COD-1", "Broken toolbar", "", nil); !strings.Contains(empty, "(no description yet)\n\n") {
		t.Errorf("missing description placeholder:\n%s", empty)
	}
}

// A file that will not fetch is named as unavailable rather than silently dropped.
func TestGrillIssuePromptNotesUnavailableFile(t *testing.T) {
	files := []attachfile.File{
		{Ref: attachfile.Ref{ID: 1, Filename: "shot.png"}, Err: errors.New("hub unreachable")},
	}
	got := grillIssuePrompt("COD-1", "Broken toolbar", "See the shot.", files)
	if !strings.Contains(got, "shot.png unavailable:") {
		t.Errorf("missing the unavailable note:\n%s", got)
	}
}
