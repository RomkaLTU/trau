package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
)

func writeSkillBody(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, ".agents", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	hash, err := agent.SkillFolderHash(dir)
	if err != nil {
		t.Fatalf("hash skill %s: %v", name, err)
	}
	return hash
}

func writeDriftLock(t *testing.T, root, pinnedHash string) {
	t.Helper()
	lock := fmt.Sprintf(`{"version":1,"skills":{
		"golang-code-style":{"source":"samber/cc-skills-golang","sourceType":"github","skillPath":"skills/golang-code-style/SKILL.md","computedHash":%q},
		"mcp-builder":{"source":"anthropics/skills","sourceType":"github","skillPath":"skills/mcp-builder/SKILL.md","computedHash":"cafebabe"}}}`, pinnedHash)
	if err := os.WriteFile(filepath.Join(root, "skills-lock.json"), []byte(lock), 0o644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}
}

func postDrift(t *testing.T, ts *httptest.Server, repo string) (int, SkillDriftInstallResponse) {
	t.Helper()
	res, err := http.Post(ts.URL+APIPrefix+"/repos/"+repo+"/skills/drift", "application/json", nil)
	if err != nil {
		t.Fatalf("POST drift: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	var out SkillDriftInstallResponse
	if res.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode drift install: %v (%s)", err, body)
		}
	}
	return res.StatusCode, out
}

// TestSkillsDriftSnapshot is the detection contract: a repo whose lockfile names
// an uninstalled skill and one edited off its pin reports both classes with the
// pinned source, and reading the snapshot never installs anything.
func TestSkillsDriftSnapshot(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	pinned := writeSkillBody(t, root, "golang-code-style", "# pinned\n")
	writeSkillBody(t, root, "golang-code-style", "# edited by hand\n")
	writeDriftLock(t, root, pinned)

	s := skillsServer(t, home)
	s.installSkill = func(context.Context, string, string) error {
		t.Error("reading the skills snapshot installed a skill")
		return nil
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	lock := getSkills(t, ts, "acme").Lock
	if !lock.Checked {
		t.Fatal("lock.checked = false, want a lockfile repo to be checked")
	}
	byName := map[string]SkillDriftView{}
	for _, d := range lock.Drift {
		byName[d.Name] = d
	}
	if got := byName["mcp-builder"]; got.Class != "missing" || got.Source != "anthropics/skills" {
		t.Errorf("mcp-builder drift = %+v, want missing with its pinned source", got)
	}
	if got := byName["golang-code-style"]; got.Class != "stale" || got.InstalledHash == got.PinnedHash {
		t.Errorf("golang-code-style drift = %+v, want stale with the on-disk hash", got)
	}
}

func TestSkillsDriftSilentWithoutLockfile(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	writeSkill(t, root, "golang-code-style")

	ts := httptest.NewServer(skillsServer(t, home).Handler())
	t.Cleanup(ts.Close)

	lock := getSkills(t, ts, "acme").Lock
	if lock.Checked || len(lock.Drift) != 0 {
		t.Errorf("lock = %+v, want no drift check without a lockfile", lock)
	}
	if status, _ := postDrift(t, ts, "acme"); status != http.StatusBadRequest {
		t.Errorf("drift install status = %d, want 400 for a repo with no lockfile", status)
	}
}

// TestSkillsDriftInstall is the gesture contract: the missing skill is installed
// and the stale one updated from the pinned source, and the snapshot the gesture
// returns has cleared the drift.
func TestSkillsDriftInstall(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	pinnedStyle := writeSkillBody(t, root, "golang-code-style", "# pinned\n")
	pinnedBuilder := writeSkillBody(t, root, "mcp-builder", "# pinned builder\n")
	writeSkillBody(t, root, "golang-code-style", "# edited by hand\n")
	if err := os.RemoveAll(filepath.Join(root, ".agents", "skills", "mcp-builder")); err != nil {
		t.Fatal(err)
	}
	lock := fmt.Sprintf(`{"version":1,"skills":{
		"golang-code-style":{"source":"samber/cc-skills-golang","sourceType":"github","computedHash":%q},
		"mcp-builder":{"source":"anthropics/skills","sourceType":"github","computedHash":%q}}}`, pinnedStyle, pinnedBuilder)
	if err := os.WriteFile(filepath.Join(root, "skills-lock.json"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}

	pinnedBodies := map[string]string{
		"samber/cc-skills-golang@golang-code-style": "# pinned\n",
		"anthropics/skills@mcp-builder":             "# pinned builder\n",
	}
	s := skillsServer(t, home)
	s.installSkill = func(_ context.Context, repoRoot, pkg string) error {
		body, ok := pinnedBodies[pkg]
		if !ok {
			return errString("unknown source " + pkg)
		}
		writeSkillBody(t, repoRoot, skillNameFromPackage(pkg), body)
		return nil
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	status, out := postDrift(t, ts, "acme")
	if status != http.StatusOK {
		t.Fatalf("drift install status = %d, want 200", status)
	}
	if len(out.Failed) != 0 {
		t.Fatalf("failed = %+v, want every pinned skill installed", out.Failed)
	}
	if len(out.Installed) != 2 {
		t.Errorf("installed = %v, want both drifted skills", out.Installed)
	}
	if len(out.Skills.Lock.Drift) != 0 {
		t.Errorf("drift after install = %+v, want it cleared", out.Skills.Lock.Drift)
	}
	if len(getSkills(t, ts, "acme").Lock.Drift) != 0 {
		t.Error("follow-up GET should report no drift")
	}
}

// TestSkillsDriftInstallHashMismatch: content that does not hash to the pin
// fails the gesture visibly and installs nothing for that skill — the drift is
// still reported afterwards.
func TestSkillsDriftInstallHashMismatch(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	pinned := writeSkillBody(t, root, "golang-code-style", "# pinned\n")
	writeSkillBody(t, root, "golang-code-style", "# edited by hand\n")
	writeDriftLock(t, root, pinned)

	s := skillsServer(t, home)
	s.installSkill = func(_ context.Context, repoRoot, pkg string) error {
		writeSkillBody(t, repoRoot, skillNameFromPackage(pkg), "# not what the lock pins\n")
		return nil
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	status, out := postDrift(t, ts, "acme")
	if status != http.StatusOK {
		t.Fatalf("drift install status = %d, want 200", status)
	}
	if len(out.Installed) != 0 {
		t.Errorf("installed = %v, want nothing accepted off the pin", out.Installed)
	}
	if len(out.Failed) != 2 {
		t.Fatalf("failed = %+v, want both skills refused", out.Failed)
	}
	for _, f := range out.Failed {
		if f.Error == "" {
			t.Errorf("failure %s carries no error", f.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "skills", "mcp-builder")); !os.IsNotExist(err) {
		t.Error("a skill that failed verification was left on disk")
	}
	if len(out.Skills.Lock.Drift) != 2 {
		t.Errorf("drift after a refused install = %+v, want it still reported", out.Skills.Lock.Drift)
	}
}

func skillNameFromPackage(pkg string) string {
	_, name, _ := strings.Cut(pkg, "@")
	return name
}
