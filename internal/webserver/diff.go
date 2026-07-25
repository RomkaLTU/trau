package webserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/registry"
)

// maxDiffPatchBytes caps the patch text one diff response carries. Past the cap the
// file entries and their line counts still come through, so an enormous slice stays
// listable without shipping megabytes the pane would never render.
const maxDiffPatchBytes = 2 << 20

// maxFilePatchBytes caps one file's patch. The total cap alone still lets a single
// generated or minified file carry the whole budget, and a patch that size costs more
// to render than the pane can absorb, so an oversized file drops to stats only.
const maxFilePatchBytes = 128 << 10

const diffHeader = "diff --git "

// errNoDiffSource is the diff endpoint's 404: the run left neither a worktree on its
// branch nor the branch itself, or its base shares no history with it, so there is
// nothing to diff.
var errNoDiffSource = errors.New("no worktree or branch to diff for this run")

// RunDiff is the /api/v1/repos/{repo}/runs/{ticket}/diff resource: everything a run
// has changed against the branch it diverged from. Source is "live" while the
// worktree still sits on the run's branch — the only view that shows uncommitted
// edits and files the agent has just created — and "committed" once the worktree has
// moved on and only the branch's history is visible.
type RunDiff struct {
	Source    string        `json:"source"`
	Base      string        `json:"base"`
	Branch    string        `json:"branch"`
	BaseSHA   string        `json:"base_sha"`
	HeadSHA   string        `json:"head_sha"`
	Truncated bool          `json:"truncated"`
	Files     []RunDiffFile `json:"files"`
}

// RunDiffFile is one changed file: its post-image path (with OldPath carrying the
// pre-image of a rename), its line counts, and its unified patch. Patch is empty for
// a binary file and for every file past the response's byte caps.
type RunDiffFile struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
	Patch     string `json:"patch"`
}

func (s *Server) handleRunDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	ticket := r.PathValue("ticket")
	s.importCheckpoints(repo)
	row, found, err := s.stores.Checkpoints().One(repo.Root, ticket)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !found || row.Branch == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no branch recorded for this run"})
		return
	}
	base, ok := s.diffBase(r.Context(), repo, row.Data)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no base branch to diff this run against"})
		return
	}
	diff, err := runDiff(r.Context(), repo.Root, base, row.Branch)
	if errors.Is(err, errNoDiffSource) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, diff)
}

// diffBase is the branch the run diverged from: its checkpoint BASE for as long as
// that branch resolves — an epic branch is deleted once its epic merges — and the
// repo's configured base branch otherwise.
func (s *Server) diffBase(ctx context.Context, repo registry.Repo, data string) (string, bool) {
	if base := checkpointField(data, "BASE"); resolvesToCommit(ctx, repo.Root, base) {
		return base, true
	}
	base := s.configuredBase(repo)
	return base, resolvesToCommit(ctx, repo.Root, base)
}

func resolvesToCommit(ctx context.Context, root, rev string) bool {
	return rev != "" && gitOutput(ctx, root, "rev-parse", "--verify", "--quiet", rev+"^{commit}") != ""
}

// configuredBase is the repo's configured base branch — the diff base for a run
// recorded before the pipeline started stamping BASE on its checkpoint.
func (s *Server) configuredBase(repo registry.Repo) string {
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		return ""
	}
	return cfg.BaseBranch
}

// runDiff computes branch's changes against base in root, preferring the working
// tree and falling back to branch's committed history when the worktree has moved
// to another branch.
func runDiff(ctx context.Context, root, base, branch string) (RunDiff, error) {
	var diff RunDiff
	var err error
	switch {
	case gitOutput(ctx, root, "rev-parse", "--abbrev-ref", "HEAD") == branch:
		diff, err = worktreeDiff(ctx, root, base, branch)
	case gitOutput(ctx, root, "rev-parse", "--verify", "refs/heads/"+branch) != "":
		diff, err = committedDiff(ctx, root, base, branch)
	default:
		return RunDiff{}, errNoDiffSource
	}
	if err != nil {
		return RunDiff{}, err
	}
	diff.Truncated = capPatches(diff.Files)
	return diff, nil
}

// worktreeDiff diffs from the fork point rather than base's tip: base advances every
// time a sibling slice merges into it, and a two-dot diff against the moved tip
// reports its new commits as deletions the run never made.
func worktreeDiff(ctx context.Context, root, base, branch string) (RunDiff, error) {
	forkPoint := gitOutput(ctx, root, "merge-base", base, "HEAD")
	if forkPoint == "" {
		return RunDiff{}, errNoDiffSource
	}
	raw, err := gitDiffOutput(ctx, root, "diff", forkPoint)
	if err != nil {
		return RunDiff{}, err
	}
	files := parseDiffFiles(raw)
	applyStats(files, numstat(ctx, root, forkPoint))
	untracked, err := untrackedDiffs(ctx, root)
	if err != nil {
		return RunDiff{}, err
	}
	return RunDiff{
		Source:  "live",
		Base:    base,
		Branch:  branch,
		BaseSHA: forkPoint,
		HeadSHA: gitOutput(ctx, root, "rev-parse", "HEAD"),
		Files:   append(files, untracked...),
	}, nil
}

func committedDiff(ctx context.Context, root, base, branch string) (RunDiff, error) {
	spec := base + "..." + branch
	raw, err := gitDiffOutput(ctx, root, "diff", spec)
	if err != nil {
		return RunDiff{}, err
	}
	files := parseDiffFiles(raw)
	applyStats(files, numstat(ctx, root, spec))
	return RunDiff{
		Source:  "committed",
		Base:    base,
		Branch:  branch,
		BaseSHA: gitOutput(ctx, root, "merge-base", base, branch),
		HeadSHA: gitOutput(ctx, root, "rev-parse", branch),
		Files:   files,
	}, nil
}

// untrackedDiffs renders every file the build has created but not staged as an
// added-file patch. `git diff --no-index` exits 1 whenever its two inputs differ,
// which is the whole point here, so its status is ignored and only the output read.
func untrackedDiffs(ctx context.Context, root string) ([]RunDiffFile, error) {
	out, err := gitDiffOutput(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	var files []RunDiffFile
	for _, path := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
		if path == "" {
			continue
		}
		raw, _ := exec.CommandContext(ctx, "git", "-C", root, "diff", "--no-index", "--", os.DevNull, path).Output()
		for _, f := range parseDiffFiles(string(raw)) {
			f.Additions, f.Deletions = countPatchLines(f.Patch)
			files = append(files, f)
		}
	}
	return files, nil
}

// gitDiffOutput runs a git command in root and surfaces its failure — unlike
// gitOutput, a diff that cannot be computed must not read as an empty diff.
func gitDiffOutput(ctx context.Context, root string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// parseDiffFiles splits raw `git diff` output into one entry per file. Only a line
// at column zero opens a new file — every line of a patch body is prefixed — and
// each chunk's extended headers are read no further than its first hunk, so a patch
// that itself contains diff lines cannot be mistaken for another file's headers.
func parseDiffFiles(raw string) []RunDiffFile {
	lines := strings.Split(raw, "\n")
	files := []RunDiffFile{}
	start := -1
	for i, line := range lines {
		if !strings.HasPrefix(line, diffHeader) {
			continue
		}
		if start >= 0 {
			files = append(files, parseDiffChunk(lines[start:i]))
		}
		start = i
	}
	if start >= 0 {
		files = append(files, parseDiffChunk(lines[start:]))
	}
	return files
}

func parseDiffChunk(lines []string) RunDiffFile {
	f := RunDiffFile{Status: "modified", Patch: strings.Join(lines, "\n")}
	old, path := headerPaths(lines[0])
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "@@") {
			break
		}
		switch {
		case strings.HasPrefix(line, "new file mode"):
			f.Status = "added"
		case strings.HasPrefix(line, "deleted file mode"):
			f.Status = "deleted"
		case strings.HasPrefix(line, "rename from "):
			old, f.Status = headerPath(line, "rename from "), "renamed"
		case strings.HasPrefix(line, "rename to "):
			path, f.Status = headerPath(line, "rename to "), "renamed"
		case strings.HasPrefix(line, "--- a/"):
			old = headerPath(line, "--- a/")
		case strings.HasPrefix(line, "+++ b/"):
			path = headerPath(line, "+++ b/")
		case strings.HasPrefix(line, "Binary files "), line == "GIT binary patch":
			f.Binary, f.Patch = true, ""
		}
	}
	f.Path = path
	if old != path {
		f.OldPath = old
	}
	return f
}

func headerPath(line, prefix string) string {
	return strings.TrimSuffix(strings.TrimPrefix(line, prefix), "\t")
}

// headerPaths reads the pre- and post-image paths from a `diff --git a/x b/y` line.
// That line is ambiguous once a path contains a space, so it is split at its
// midpoint — exact whenever both sides are the same length, which covers everything
// the ---/+++ and rename headers do not already correct.
func headerPaths(line string) (old, path string) {
	rest := strings.TrimPrefix(line, diffHeader)
	if half := len(rest) / 2; half > 1 && rest[half] == ' ' {
		return strings.TrimPrefix(rest[:half], "a/"), strings.TrimPrefix(rest[half+1:], "b/")
	}
	_, post, ok := strings.Cut(rest, " b/")
	if !ok {
		return "", ""
	}
	return post, post
}

func countPatchLines(patch string) (additions, deletions int) {
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			additions++
		case strings.HasPrefix(line, "-"):
			deletions++
		}
	}
	return additions, deletions
}

type diffStat struct {
	additions int
	deletions int
}

// numstat reads a diff's per-file line counts, keyed by post-image path. The -z form
// is what makes renames unambiguous: git leaves the path field empty and emits the
// pre- and post-image paths as the two following records. A binary file reports "-",
// which reads as zero.
func numstat(ctx context.Context, root string, spec string) map[string]diffStat {
	out := gitOutput(ctx, root, "diff", "--numstat", "-z", spec)
	fields := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	stats := map[string]diffStat{}
	for i := 0; i < len(fields); i++ {
		adds, rest, ok := strings.Cut(fields[i], "\t")
		if !ok {
			continue
		}
		dels, path, ok := strings.Cut(rest, "\t")
		if !ok {
			continue
		}
		if path == "" {
			if i+2 >= len(fields) {
				break
			}
			path = fields[i+2]
			i += 2
		}
		additions, _ := strconv.Atoi(adds)
		deletions, _ := strconv.Atoi(dels)
		stats[path] = diffStat{additions: additions, deletions: deletions}
	}
	return stats
}

func applyStats(files []RunDiffFile, stats map[string]diffStat) {
	for i := range files {
		if st, ok := stats[files[i].Path]; ok {
			files[i].Additions, files[i].Deletions = st.additions, st.deletions
		}
	}
}

// capPatches drops the patch text of every file over the per-file cap and of every
// file that would carry the response past the total cap, leaving the file entries and
// their line counts in place.
func capPatches(files []RunDiffFile) bool {
	total, truncated := 0, false
	for i := range files {
		size := len(files[i].Patch)
		if size > maxFilePatchBytes || total+size > maxDiffPatchBytes {
			files[i].Patch, truncated = "", true
			continue
		}
		total += size
	}
	return truncated
}
