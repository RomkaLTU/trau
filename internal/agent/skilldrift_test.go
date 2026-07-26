package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkillFiles(t *testing.T, repo, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(repo, ".agents/skills", name)
	for rel, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func writeSkillsLock(t *testing.T, repo string, pins map[string]SkillLock) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"version": 1, "skills": pins})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, SkillsLockFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSkillFolderHashMatchesLockfileAlgorithm pins the hash to what the
// skills.sh CLI writes into computedHash: path-then-content over every file,
// ordered case-insensitively, so LICENSE.txt and references/ sort ahead of
// SKILL.md rather than behind it as a byte comparison would put them.
func TestSkillFolderHashMatchesLockfileAlgorithm(t *testing.T) {
	repo := t.TempDir()
	dir := writeSkillFiles(t, repo, "demo", map[string]string{
		"SKILL.md":        "# demo\n",
		"LICENSE.txt":     "license\n",
		"references/a.md": "alpha\n",
	})

	got, err := SkillFolderHash(dir)
	if err != nil {
		t.Fatalf("SkillFolderHash: %v", err)
	}
	const want = "603ec39bb8e5165ca929350375857f381c3f9899ec95a7a063aaeba9a4a2d27f"
	if got != want {
		t.Errorf("SkillFolderHash = %s, want %s", got, want)
	}
}

// TestCheckSkillDriftClasses: a locked skill that is not installed reads as
// missing, one whose content moved off the pin reads as stale, one at its pinned
// content is silent, and an installed skill with no pin is reported without ever
// being drift.
func TestCheckSkillDriftClasses(t *testing.T) {
	repo := t.TempDir()
	pinned := writeSkillFiles(t, repo, "pinned", map[string]string{"SKILL.md": "# pinned\n"})
	writeSkillFiles(t, repo, "edited", map[string]string{"SKILL.md": "# edited by hand\n"})
	writeSkillFiles(t, repo, "hand-rolled", map[string]string{"SKILL.md": "# hand-rolled\n"})
	pinnedHash, err := SkillFolderHash(pinned)
	if err != nil {
		t.Fatalf("SkillFolderHash: %v", err)
	}
	writeSkillsLock(t, repo, map[string]SkillLock{
		"pinned": {Source: "acme/skills", SourceType: "github", SkillPath: "skills/pinned/SKILL.md", Hash: pinnedHash},
		"edited": {Source: "acme/skills", SourceType: "github", SkillPath: "skills/edited/SKILL.md", Hash: "deadbeef"},
		"absent": {Source: "other/skills", SourceType: "github", SkillPath: "skills/absent/SKILL.md", Hash: "cafebabe"},
	})

	report := CheckSkillDrift(repo)
	if !report.Checked {
		t.Fatal("report.Checked = false, want a lockfile repo to be checked")
	}
	if len(report.Drifted) != 2 {
		t.Fatalf("drifted = %+v, want the missing and the stale skill", report.Drifted)
	}
	if got := report.Drifted[0]; got.Name != "absent" || got.Class != SkillDriftMissing || got.Lock.Source != "other/skills" {
		t.Errorf("first drift = %+v, want absent reported missing with its pinned source", got)
	}
	if got := report.Drifted[1]; got.Name != "edited" || got.Class != SkillDriftStale || got.Installed == "" {
		t.Errorf("second drift = %+v, want edited reported stale with its on-disk hash", got)
	}
	if len(report.Unlocked) != 1 || report.Unlocked[0] != "hand-rolled" {
		t.Errorf("unlocked = %v, want the unpinned skill reported", report.Unlocked)
	}
}

func TestCheckSkillDriftWithoutLockfile(t *testing.T) {
	repo := t.TempDir()
	writeSkillFiles(t, repo, "pinned", map[string]string{"SKILL.md": "# pinned\n"})

	report := CheckSkillDrift(repo)
	if report.Checked || len(report.Drifted) != 0 || len(report.Unlocked) != 0 {
		t.Errorf("report = %+v, want no check at all without a lockfile", report)
	}
}

// TestInstallPinnedSkillVerifiesHash: the gesture keeps content that hashes to
// the pin, and refuses content that does not — leaving a missing skill missing
// and a stale skill exactly as it was.
func TestInstallPinnedSkillVerifiesHash(t *testing.T) {
	t.Run("matching content is installed", func(t *testing.T) {
		repo := t.TempDir()
		want, err := SkillFolderHash(writeSkillFiles(t, repo, "probe", map[string]string{"SKILL.md": "# pinned\n"}))
		if err != nil {
			t.Fatalf("SkillFolderHash: %v", err)
		}
		if err := os.RemoveAll(filepath.Join(repo, ".agents/skills/probe")); err != nil {
			t.Fatal(err)
		}

		install := func(_ context.Context, repoRoot, pkg string) error {
			if pkg != "acme/skills@probe" {
				t.Errorf("install pkg = %q, want the pinned source and skill", pkg)
			}
			writeSkillFiles(t, repoRoot, "probe", map[string]string{"SKILL.md": "# pinned\n"})
			return nil
		}
		pin := SkillLock{Source: "acme/skills", Hash: want}
		if err := InstallPinnedSkill(t.Context(), repo, "probe", pin, install); err != nil {
			t.Fatalf("InstallPinnedSkill: %v", err)
		}
		if drift := CheckSkillDrift(repo); len(drift.Drifted) != 0 {
			t.Errorf("drift after install = %+v, want none", drift.Drifted)
		}
	})

	t.Run("mismatched content installs nothing", func(t *testing.T) {
		repo := t.TempDir()
		install := func(_ context.Context, repoRoot, _ string) error {
			writeSkillFiles(t, repoRoot, "probe", map[string]string{"SKILL.md": "# something else\n"})
			return nil
		}
		pin := SkillLock{Source: "acme/skills", Hash: "cafebabe"}
		err := InstallPinnedSkill(t.Context(), repo, "probe", pin, install)
		if err == nil {
			t.Fatal("InstallPinnedSkill = nil, want a hash mismatch error")
		}
		if !strings.Contains(err.Error(), "cafebabe") {
			t.Errorf("error = %v, want it to name the pinned hash", err)
		}
		if _, ok := installedSkillDirs(repo)["probe"]; ok {
			t.Error("a skill that failed verification was left installed")
		}
	})

	t.Run("mismatched content leaves the installed skill untouched", func(t *testing.T) {
		repo := t.TempDir()
		dir := writeSkillFiles(t, repo, "probe", map[string]string{"SKILL.md": "# on disk\n"})
		before, err := SkillFolderHash(dir)
		if err != nil {
			t.Fatalf("SkillFolderHash: %v", err)
		}

		install := func(_ context.Context, repoRoot, _ string) error {
			writeSkillFiles(t, repoRoot, "probe", map[string]string{"SKILL.md": "# tampered\n"})
			return nil
		}
		pin := SkillLock{Source: "acme/skills", Hash: "cafebabe"}
		if err := InstallPinnedSkill(t.Context(), repo, "probe", pin, install); err == nil {
			t.Fatal("InstallPinnedSkill = nil, want a hash mismatch error")
		}
		after, err := SkillFolderHash(filepath.Join(repo, ".agents/skills/probe"))
		if err != nil {
			t.Fatalf("SkillFolderHash after rollback: %v", err)
		}
		if after != before {
			t.Errorf("skill content changed under a failed install: %s -> %s", before, after)
		}
	})

	// The skills.sh CLI re-pins the lockfile to whatever it fetched. Letting that
	// stand would move the pin the fetch is measured against, so a refused
	// install must leave the checked-in lockfile exactly as it was.
	t.Run("the lockfile the CLI rewrites is put back", func(t *testing.T) {
		repo := t.TempDir()
		pins := map[string]SkillLock{"probe": {Source: "acme/skills", Hash: "cafebabe"}}
		writeSkillsLock(t, repo, pins)
		before, err := os.ReadFile(filepath.Join(repo, SkillsLockFile))
		if err != nil {
			t.Fatal(err)
		}

		install := func(_ context.Context, repoRoot, _ string) error {
			writeSkillFiles(t, repoRoot, "probe", map[string]string{"SKILL.md": "# fetched\n"})
			dir := filepath.Join(repoRoot, ".agents/skills/probe")
			hash, err := SkillFolderHash(dir)
			if err != nil {
				return err
			}
			writeSkillsLock(t, repoRoot, map[string]SkillLock{"probe": {Source: "acme/skills", Hash: hash}})
			return nil
		}
		if err := InstallPinnedSkill(t.Context(), repo, "probe", pins["probe"], install); err == nil {
			t.Fatal("InstallPinnedSkill = nil, want a hash mismatch error")
		}
		after, err := os.ReadFile(filepath.Join(repo, SkillsLockFile))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("lockfile was rewritten by the install:\n%s\nwant:\n%s", after, before)
		}
	})

	t.Run("a failed install restores the skill", func(t *testing.T) {
		repo := t.TempDir()
		writeSkillFiles(t, repo, "probe", map[string]string{"SKILL.md": "# on disk\n"})
		install := func(context.Context, string, string) error {
			return errSkillInstall
		}
		if err := InstallPinnedSkill(t.Context(), repo, "probe", SkillLock{Source: "acme/skills"}, install); err == nil {
			t.Fatal("InstallPinnedSkill = nil, want the installer's error")
		}
		if _, ok := installedSkillDirs(repo)["probe"]; !ok {
			t.Error("the installed skill did not come back after a failed install")
		}
	})
}

var errSkillInstall = errString("install skill probe: exit status 1: unknown source")

type errString string

func (e errString) Error() string { return string(e) }
