package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Drift classes for a locked skill: missing when nothing is installed under
// that name, stale when the installed content hashes to something other than
// the lock's computedHash.
const (
	SkillDriftMissing = "missing"
	SkillDriftStale   = "stale"
)

// SkillDrift is one locked skill whose installed state does not match the pin.
// Installed is the hash the skill currently has on disk, empty for a missing
// skill or a directory that cannot be read.
type SkillDrift struct {
	Name      string
	Class     string
	Lock      SkillLock
	Installed string
}

// SkillDriftReport is a repo's skills measured against skills-lock.json.
// Checked is false when the repo pins nothing — the lockfile's presence is the
// whole opt-in, so an unlocked repo is silent rather than warned about.
// Unlocked names the skills installed without a pin: reported, never removed.
type SkillDriftReport struct {
	Checked  bool
	Drifted  []SkillDrift
	Unlocked []string
}

// CheckSkillDrift compares each pinned skill against the skills the repo
// actually has installed, over the same discovery the resolver routes on.
func CheckSkillDrift(repoRoot string) SkillDriftReport {
	lock := ReadSkillsLock(repoRoot)
	if len(lock) == 0 {
		return SkillDriftReport{}
	}
	dirs := installedSkillDirs(repoRoot)
	report := SkillDriftReport{Checked: true}
	for _, name := range slices.Sorted(maps.Keys(lock)) {
		pin := lock[name]
		dir, ok := dirs[name]
		if !ok {
			report.Drifted = append(report.Drifted, SkillDrift{Name: name, Class: SkillDriftMissing, Lock: pin})
			continue
		}
		hash, err := SkillFolderHash(dir)
		if err == nil && (pin.Hash == "" || hash == pin.Hash) {
			continue
		}
		report.Drifted = append(report.Drifted, SkillDrift{
			Name:      name,
			Class:     SkillDriftStale,
			Lock:      pin,
			Installed: hash,
		})
	}
	for _, name := range sortedSkillNames(dirs) {
		if _, ok := lock[name]; !ok {
			report.Unlocked = append(report.Unlocked, name)
		}
	}
	return report
}

// SkillInstaller installs a `owner/repo@skill` package spec into a repo —
// InstallSkillPackage in production, taken as a parameter so it can be faked.
type SkillInstaller func(ctx context.Context, repoRoot, pkg string) error

// InstallPinnedSkill installs one locked skill from its pinned source and keeps
// the result only when it hashes to the lock's computedHash. The skill's current
// directory is set aside for the attempt and restored on any failure, so content
// that does not match the pin is never left on the machine. The lockfile is
// input and never output: the skills.sh CLI re-pins whatever it fetched, which
// would move the pin the fetch was being measured against.
func InstallPinnedSkill(ctx context.Context, repoRoot, name string, pin SkillLock, install SkillInstaller) error {
	defer restoreSkillsLock(repoRoot)()

	before := installedSkillDirs(repoRoot)[name]
	stash, err := stashSkillDir(before)
	if err != nil {
		return err
	}
	if err := installAndVerify(ctx, repoRoot, name, pin, install); err != nil {
		rollbackSkillDir(repoRoot, name, before, stash)
		return err
	}
	_ = os.RemoveAll(stash)
	return nil
}

func installAndVerify(ctx context.Context, repoRoot, name string, pin SkillLock, install SkillInstaller) error {
	if err := install(ctx, repoRoot, pin.Source+"@"+name); err != nil {
		return err
	}
	dir := installedSkillDirs(repoRoot)[name]
	if dir == "" {
		return fmt.Errorf("install %s: %s installed nothing under that name", name, pin.Source)
	}
	hash, err := SkillFolderHash(dir)
	if err != nil {
		return err
	}
	if hash != pin.Hash {
		return fmt.Errorf("install %s: fetched content hashes to %s, not the pinned %s", name, hash, pin.Hash)
	}
	return nil
}

// restoreSkillsLock captures skills-lock.json and returns the call that puts it
// back byte for byte, so an install leaves the repo's checked-in pins alone.
func restoreSkillsLock(repoRoot string) func() {
	path := filepath.Join(repoRoot, SkillsLockFile)
	before, err := os.ReadFile(path)
	if err != nil {
		return func() {}
	}
	return func() {
		after, err := os.ReadFile(path)
		if err == nil && !bytes.Equal(after, before) {
			_ = os.WriteFile(path, before, 0o644)
		}
	}
}

// stashSkillDir moves an installed skill out of the way of a reinstall, under a
// dot-prefixed sibling name so discovery cannot pick it up while it sits there.
func stashSkillDir(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	name := filepath.Base(dir)
	stash := filepath.Join(filepath.Dir(dir), "."+name+".trau-stash")
	if err := os.RemoveAll(stash); err != nil {
		return "", fmt.Errorf("clear stash for skill %s: %w", name, err)
	}
	if err := os.Rename(dir, stash); err != nil {
		return "", fmt.Errorf("set aside skill %s: %w", name, err)
	}
	return stash, nil
}

func rollbackSkillDir(repoRoot, name, before, stash string) {
	if dir := installedSkillDirs(repoRoot)[name]; dir != "" {
		_ = os.RemoveAll(dir)
	}
	if stash != "" {
		_ = os.Rename(stash, before)
	}
}

// SkillFolderHash hashes an installed skill the way the skills.sh CLI computes
// the lockfile's computedHash: every file under the skill directory, ordered
// case-insensitively by relative path, each contributing its path and then its
// bytes. .git and node_modules are skipped, as they are by the CLI.
func SkillFolderHash(dir string) (string, error) {
	var rels []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && (d.Name() == ".git" || d.Name() == "node_modules") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("read skill %s: %w", filepath.Base(dir), err)
	}
	sort.Slice(rels, func(i, j int) bool {
		a, b := strings.ToLower(rels[i]), strings.ToLower(rels[j])
		if a == b {
			return rels[i] < rels[j]
		}
		return a < b
	})

	sum := sha256.New()
	for _, rel := range rels {
		content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", fmt.Errorf("read skill %s: %w", filepath.Base(dir), err)
		}
		_, _ = sum.Write([]byte(rel))
		_, _ = sum.Write(content)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
