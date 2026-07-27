package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/attachfile"
	"github.com/RomkaLTU/trau/internal/prompts"
	"github.com/RomkaLTU/trau/internal/proofs"
)

// Every file the phases hand each other must resolve inside the OS temp
// directory. A hardcoded "/tmp/..." still compiles for Windows, so the
// cross-compile gate cannot catch one — this test is what does.
func TestAgentInterfacePathsAreTempRooted(t *testing.T) {
	const id = "COD-1268"
	tmp := filepath.Clean(os.TempDir())

	for _, tc := range []struct {
		name string
		path string
	}{
		{"handoff brief", handoffPath(id)},
		{"verdict", verifyPath(id)},
		{"panel member verdict", verifyMemberPath(id, "codex")},
		{"rubric", rubricPath(id)},
		{"distilled lesson", lessonDistillPath(id)},
		{"timelog estimate", timelogEstimatePath(id)},
		{"build notes", buildNotesPath(id)},
		{"qa capture", qaCapturePath(id)},
		{"proofs dir", proofs.Dir(id)},
		{"attachments dir", attachfile.Dir(id)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := filepath.Dir(tc.path); got != tmp {
				t.Errorf("%s = %q, want it directly under the OS temp dir %q", tc.name, tc.path, tmp)
			}
		})
	}
}

// The verify prompt's browser-proofs contract must name the same directory the
// loop harvests from, so a temp directory that is not /tmp cannot split them.
func TestVerifyPromptNamesTheHarvestedProofsDir(t *testing.T) {
	const id = "COD-1268"
	tail := verifyTail(prompts.Renderer{}, id, handoffPath(id), verifyPath(id), "", "", "", "", "", "skills", "", "", true)
	mustContain(t, "verifyTail", tail, proofs.Dir(id)+"/manifest.json")
}
