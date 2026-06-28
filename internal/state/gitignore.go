package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// EnsureGitignore makes the target repo ignore trau's run artifacts and project
// config so they never show as untracked clutter and — critically — survive the
// quarantine path's `git clean -fd` (gitignored dirs are not removed without -x,
// and the live state store lives under the runs dir). It ignores the configured
// runs dir and .trau.ini, but never .trau/ wholesale: .trau/checks is meant to be
// committed. Best-effort and idempotent — it creates .gitignore when missing and
// skips any pattern an existing rule already covers, so it writes at most once.
func EnsureGitignore(repoRoot, runsDir string) error {
	if strings.TrimSpace(repoRoot) == "" {
		return nil
	}
	gi := filepath.Join(repoRoot, ".gitignore")
	data, err := os.ReadFile(gi)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	existing := gitignoreEntries(string(data))

	var add []string
	if rel := relativeRunsDir(runsDir); rel != "" && !gitignoreCoversDir(existing, rel) {
		add = append(add, rel+"/")
	}
	if !existing[".trau.ini"] {
		add = append(add, ".trau.ini")
	}
	if len(add) == 0 {
		return nil
	}

	var b strings.Builder
	b.Write(data)
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("# trau run artifacts and project config (do not commit)\n")
	for _, p := range add {
		b.WriteString(p + "\n")
	}
	return os.WriteFile(gi, []byte(b.String()), 0o644)
}

// gitignoreEntries collects the non-comment ignore patterns, each trimmed of
// surrounding whitespace and a single leading/trailing slash, into a set.
func gitignoreEntries(content string) map[string]bool {
	set := map[string]bool{}
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSuffix(strings.TrimPrefix(line, "/"), "/")
		if line != "" {
			set[line] = true
		}
	}
	return set
}

// relativeRunsDir returns the repo-relative, slash-normalized runs dir, or "" when
// the configured value is absolute or escapes the repo — neither expresses as a
// repo-local .gitignore pattern, so those are left for the user to ignore.
func relativeRunsDir(runsDir string) string {
	d := strings.TrimSpace(runsDir)
	if d == "" || filepath.IsAbs(d) {
		return ""
	}
	d = filepath.ToSlash(filepath.Clean(d))
	if d == "." || d == ".." || strings.HasPrefix(d, "../") {
		return ""
	}
	return d
}

// gitignoreCoversDir reports whether the runs dir or any ancestor is already
// ignored — e.g. an existing ".trau" rule covers ".trau/runs".
func gitignoreCoversDir(existing map[string]bool, rel string) bool {
	for p := rel; p != ""; {
		if existing[p] {
			return true
		}
		i := strings.LastIndex(p, "/")
		if i < 0 {
			return false
		}
		p = p[:i]
	}
	return false
}
