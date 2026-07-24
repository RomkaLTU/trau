package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/skillrules"
)

func repoWithRules(t *testing.T, names []string, rules ...skillrules.Rule) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		dir := filepath.Join(root, ".claude", "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: d\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := skillrules.Save(root, skillrules.Set{Rules: rules}); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestPlannedSkillsReceipt: every phase attempt that names a skill set files it
// with the step that produced it, so plan-versus-loaded coverage is comparable
// against the skills the agent reports having loaded. A repo that installs no
// skills plans nothing and files nothing.
func TestPlannedSkillsReceipt(t *testing.T) {
	t.Run("a build files its planned set and reason", func(t *testing.T) {
		id := "COD-1"
		var buf bytes.Buffer
		p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
		p.Events = event.New(&buf)
		p.RepoRoot = repoWithRules(t, []string{"golang-code-style", "github-release"},
			skillrules.Rule{Skill: "golang-code-style", Scope: skillrules.ScopeAlways},
			skillrules.Rule{Skill: "github-release", Scope: skillrules.ScopeManual},
		)

		if err := p.Build(context.Background(), id); err != nil {
			t.Fatalf("Build: %v", err)
		}

		evs := kindEvents(t, &buf, event.KindSkillsPlanned)
		if len(evs) != 1 {
			t.Fatalf("emitted %d skills_planned events, want 1", len(evs))
		}
		ev := evs[0]
		if ev.Phase != "build" {
			t.Errorf("phase = %q, want build", ev.Phase)
		}
		if got := strField(ev.Fields, "ticket"); got != id {
			t.Errorf("ticket = %q, want %q", got, id)
		}
		if got := strField(ev.Fields, "source"); got != agent.SkillsSourceRules {
			t.Errorf("source = %q, want %q", got, agent.SkillsSourceRules)
		}
		if got := skillsField(t, ev); len(got) != 1 || got[0] != "golang-code-style" {
			t.Errorf("skills = %v, want the always skill alone", got)
		}
	})

	t.Run("a repo with no skills files nothing", func(t *testing.T) {
		var buf bytes.Buffer
		p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
		p.Events = event.New(&buf)
		p.RepoRoot = t.TempDir()

		if err := p.Build(context.Background(), "COD-2"); err != nil {
			t.Fatalf("Build: %v", err)
		}
		if evs := kindEvents(t, &buf, event.KindSkillsPlanned); len(evs) != 0 {
			t.Errorf("emitted %d skills_planned events, want 0", len(evs))
		}
	})
}

func skillsField(t *testing.T, ev event.Event) []string {
	t.Helper()
	raw, err := json.Marshal(ev.Fields["skills"])
	if err != nil {
		t.Fatalf("marshal skills field: %v", err)
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode skills field: %v", err)
	}
	return out
}
