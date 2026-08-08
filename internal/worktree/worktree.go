// Package worktree provisions, adopts and removes the per-ticket git worktrees a
// run works in when WORKTREES=1 (ADR 0044).
//
// A tree lives at <dir>/<repo-name>/<ticket-id>, computed by Path from
// configuration alone so the loop, the hub and the CLI all name the same
// directory without passing it around. Provision is idempotent: a healthy tree
// already at that path is adopted with its work-in-progress intact, and only a
// freshly created one runs the copy and setup steps. Remove is the settle side,
// and it always prunes so git's registry never keeps a tree the disk has lost.
//
// Nothing here touches the registered checkout's working tree. That is the whole
// point of the feature: with worktrees on, a run never stashes, resets or checks
// anything out in the tree the user is standing in.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/folderrepo"
)

// gitWaitDelay bounds how long a cancelled git command may keep the pipe open,
// matching the pipeline's own git runner.
const gitWaitDelay = 2 * time.Second

// Path is the deterministic location of a ticket's worktree: one directory per
// repo under dir, one per ticket inside it. Both components are reduced to their
// base name, so neither a repo root nor a slash-bearing ticket id can climb out of
// dir. An empty dir, repo or ticket yields "" — the caller has nothing to act on.
func Path(dir, repo, ticket string) string {
	dir, repo, ticket = strings.TrimSpace(dir), strings.TrimSpace(repo), strings.TrimSpace(ticket)
	if dir == "" || repo == "" || ticket == "" {
		return ""
	}
	return filepath.Join(dir, filepath.Base(repo), filepath.Base(ticket))
}

// Entry is one tree in git's own worktree registry.
type Entry struct {
	Path   string
	Branch string
	// Detached marks a tree holding a commit rather than a branch — how a fresh
	// tree starts before the ticket's branch is cut inside it.
	Detached bool
}

// HeldError reports that the ticket's branch is checked out in another tree — the
// user's own checkout, or a worktree an earlier run left behind. git allows a
// branch exactly one checkout across a repo, so this is a park, not a retry: the
// operator has to decide which tree keeps the branch.
type HeldError struct {
	Branch string
	By     string
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("branch %s is already checked out in %s — a branch can live in one working tree at a time; finish or remove that tree first", e.Branch, e.By)
}

// SetupError reports a WORKTREE_SETUP_CMD that exited non-zero. Output carries
// what the command printed so the run can keep it as an artifact, and the tree is
// deliberately left in place for whoever reads that output.
type SetupError struct {
	Cmd    string
	Tree   string
	Output string
	Err    error
}

func (e *SetupError) Error() string {
	return fmt.Sprintf("worktree setup command failed in %s: %v", e.Tree, e.Err)
}

func (e *SetupError) Unwrap() error { return e.Err }

// Options is everything Provision needs to place one ticket's tree.
type Options struct {
	// RepoRoot is the registered checkout that owns the git directory every tree
	// of this repo shares. All git commands run against it.
	RepoRoot string
	// Dir is the resolved worktrees root (config WorktreesRoot).
	Dir string
	// Repo is the repo's display name — the directory level under Dir.
	Repo string
	// Ticket is the ticket id — the tree's own directory.
	Ticket string
	// Branch is the ticket's branch when one already exists locally, which makes
	// this a resume: the tree is created holding that branch, WIP and all. Empty
	// starts the tree detached at the base tip and leaves the branch to be cut
	// inside it.
	Branch string
	// Base and Remote name the base branch a fresh tree starts at; the
	// remote-tracking tip is preferred, the local branch is the fallback.
	Base   string
	Remote string
	// Copy holds the WORKTREE_COPY globs; RunsDir is excluded from the copied
	// .trau/ tree so a run never inherits another run's artifacts.
	Copy    []string
	RunsDir string
	// SetupCmd is WORKTREE_SETUP_CMD, run under `sh -c` in a fresh tree.
	SetupCmd string
	// Logf, when set, narrates provisioning into the run's log.
	Logf func(format string, args ...any)
}

// Result is what Provision left behind.
type Result struct {
	// Path is the tree, whether it was created now or adopted.
	Path string
	// Branch is the branch the tree holds, empty when it started detached.
	Branch string
	// Created distinguishes a fresh tree — which ran the copy and setup steps —
	// from one adopted with its existing work.
	Created bool
}

// Provision puts the ticket's worktree in place and reports what it found or made.
//
// It fetches first so a fresh tree starts at the real base tip, then either adopts
// the healthy tree already at the path or creates one: holding the ticket's branch
// when that branch exists, detached at the base tip when it does not. A branch some
// other tree holds is a *HeldError rather than git's cryptic refusal. The copy and
// setup steps run only on creation, so a resume neither overwrites the tree's files
// nor pays for setup again.
func Provision(ctx context.Context, o Options) (Result, error) {
	path := Path(o.Dir, o.Repo, o.Ticket)
	if path == "" {
		return Result{}, errors.New("worktrees are on but no worktree path could be computed — set WORKTREES_DIR")
	}
	if err := ValidateDir(o.Dir); err != nil {
		return Result{}, err
	}
	if o.RepoRoot == "" {
		return Result{}, errors.New("worktree provisioning needs a registered repo root")
	}
	// ValidateDir catches a directory inside a git repository; a folder repo is not
	// one, so its own root is checked here, where it is known.
	if inside(o.RepoRoot, o.Dir) {
		return Result{}, fmt.Errorf("worktrees directory %s sits inside the registered repo %s — point WORKTREES_DIR outside it (the default <TRAU_HOME>/worktrees is)", o.Dir, o.RepoRoot)
	}

	// A tree git forgot but the disk kept is the ordinary crash leftover; prune
	// first so the registry and the disk are compared on equal terms.
	_ = run(ctx, o.RepoRoot, "worktree", "prune")

	adopted, err := adoptable(ctx, o.RepoRoot, path)
	if err != nil {
		return Result{}, err
	}
	if adopted != nil {
		o.logf("  ↳ adopting the existing worktree at %s", path)
		return Result{Path: path, Branch: adopted.Branch, Created: false}, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{}, fmt.Errorf("create worktrees directory %s: %w", filepath.Dir(path), err)
	}
	// Best effort: an unreachable remote must leave a run able to work off what
	// the local repo already has.
	if o.Remote != "" && o.Base != "" {
		if ferr := run(ctx, o.RepoRoot, "fetch", o.Remote, o.Base); ferr != nil {
			o.logf("  ⚠ fetch %s %s before provisioning the worktree: %v", o.Remote, o.Base, ferr)
		}
	}

	branch := o.Branch
	if branch != "" {
		if held, herr := Holding(ctx, o.RepoRoot, branch); herr == nil && held != "" {
			return Result{}, &HeldError{Branch: branch, By: held}
		}
		if !branchExists(ctx, o.RepoRoot, branch) {
			branch = ""
		}
	}

	if branch != "" {
		if err := run(ctx, o.RepoRoot, "worktree", "add", path, branch); err != nil {
			return Result{}, fmt.Errorf("git worktree add %s %s: %w", path, branch, err)
		}
		o.logf("  ↳ worktree %s ← branch %s", path, branch)
	} else {
		start := startPoint(ctx, o.RepoRoot, o.Remote, o.Base)
		if err := run(ctx, o.RepoRoot, "worktree", "add", "--detach", path, start); err != nil {
			return Result{}, fmt.Errorf("git worktree add --detach %s %s: %w", path, start, err)
		}
		o.logf("  ↳ worktree %s ← %s (detached; the ticket's branch is cut inside it)", path, start)
	}

	if err := Copy(ctx, o.RepoRoot, path, o.Copy, o.RunsDir); err != nil {
		return Result{}, err
	}
	if err := Setup(ctx, path, o.SetupCmd); err != nil {
		return Result{Path: path, Branch: branch, Created: true}, err
	}
	return Result{Path: path, Branch: branch, Created: true}, nil
}

// Remove takes a tree out of git's registry and off the disk, then prunes so the
// registry cannot keep a stale record either way. A tree git no longer knows about
// is finished by hand — but only when the directory still looks like a worktree, so
// a mistyped path can never delete something else.
func Remove(ctx context.Context, repoRoot, path string) error {
	if repoRoot == "" || path == "" {
		return nil
	}
	rmErr := run(ctx, repoRoot, "worktree", "remove", "--force", path)
	if rmErr != nil {
		switch _, statErr := os.Stat(path); {
		case os.IsNotExist(statErr):
			rmErr = nil
		case folderrepo.IsRepo(path):
			if err := os.RemoveAll(path); err == nil {
				rmErr = nil
			}
		}
	}
	return errors.Join(rmErr, Prune(ctx, repoRoot))
}

// Prune drops git's records of worktrees whose directories are gone.
func Prune(ctx context.Context, repoRoot string) error {
	if repoRoot == "" {
		return nil
	}
	if err := run(ctx, repoRoot, "worktree", "prune"); err != nil {
		return fmt.Errorf("git worktree prune in %s: %w", repoRoot, err)
	}
	return nil
}

// List reports the trees git knows about for repoRoot, the main checkout included.
func List(ctx context.Context, repoRoot string) ([]Entry, error) {
	out, err := output(ctx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list in %s: %w", repoRoot, err)
	}
	entries := []Entry{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			entries = append(entries, Entry{Path: strings.TrimPrefix(line, "worktree ")})
		case len(entries) == 0:
			continue
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			entries[len(entries)-1].Branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "detached":
			entries[len(entries)-1].Detached = true
		}
	}
	return entries, nil
}

// Holding returns the path of the tree that has branch checked out, or "" when no
// tree does. The main checkout counts: it is the commonest holder of all, and the
// reason a run has to park rather than fight git for the branch.
func Holding(ctx context.Context, repoRoot, branch string) (string, error) {
	if branch == "" {
		return "", nil
	}
	entries, err := List(ctx, repoRoot)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Branch == branch {
			return e.Path, nil
		}
	}
	return "", nil
}

// ValidateDir refuses a worktrees root that sits inside a git repository. A tree
// under a registered repo — or under one of a folder repo's children — would be
// part of the very checkout it exists to stay out of: the repo's status could never
// read clean, and folderrepo.IsRepo would take each tree's `.git` file for yet
// another repository.
func ValidateDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errors.New("no worktrees directory resolved — set WORKTREES_DIR, or TRAU_HOME so it can default to <TRAU_HOME>/worktrees")
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("worktrees directory %s must be an absolute path", dir)
	}
	for cur := filepath.Clean(dir); ; {
		if folderrepo.IsRepo(cur) {
			return fmt.Errorf("worktrees directory %s sits inside the git repository %s — point WORKTREES_DIR outside every repo (the default <TRAU_HOME>/worktrees is)", dir, cur)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil
		}
		cur = parent
	}
}

// Copy fills a fresh tree with what git cannot carry into it.
//
// trau's own files come across unconditionally — the project config, the custom
// checks and prompts under .trau/, the repo-pinned git identity, and an untracked
// .agents/ — because a tree without them is not the same repo to a run. Everything
// else has to be both matched by a WORKTREE_COPY glob and ignored by git at the
// root: tracked content already arrived with the checkout, so copying it would only
// shadow the commit the tree is on. A glob matching nothing is silently fine.
//
// runsDir, when it lives under .trau/, is skipped: a fresh tree starts with no run
// artifacts, least of all another ticket's.
func Copy(ctx context.Context, root, tree string, globs []string, runsDir string) error {
	var errs []error
	for _, name := range []string{".trau.ini", ".gitconfig.repo"} {
		if err := copyPath(filepath.Join(root, name), filepath.Join(tree, name), nil); err != nil {
			errs = append(errs, err)
		}
	}
	skip := map[string]bool{}
	if runsDir != "" && !filepath.IsAbs(runsDir) {
		skip[filepath.Clean(filepath.Join(root, runsDir))] = true
	}
	if err := copyPath(filepath.Join(root, ".trau"), filepath.Join(tree, ".trau"), skip); err != nil {
		errs = append(errs, err)
	}
	if untracked(ctx, root, ".agents") {
		if err := copyPath(filepath.Join(root, ".agents"), filepath.Join(tree, ".agents"), nil); err != nil {
			errs = append(errs, err)
		}
	}
	for _, glob := range globs {
		matches, err := filepath.Glob(filepath.Join(root, glob))
		if err != nil {
			errs = append(errs, fmt.Errorf("worktree copy glob %q: %w", glob, err))
			continue
		}
		for _, match := range matches {
			rel, err := filepath.Rel(root, match)
			if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
				continue
			}
			if !ignored(ctx, root, rel) {
				continue
			}
			if err := copyPath(match, filepath.Join(tree, rel), nil); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Setup runs WORKTREE_SETUP_CMD under `sh -c` with the fresh tree as its working
// directory. A non-zero exit is a *SetupError carrying the command's combined
// output; the tree is left in place so that output can be acted on.
func Setup(ctx context.Context, tree, cmd string) error {
	if strings.TrimSpace(cmd) == "" {
		return nil
	}
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Dir = tree
	c.WaitDelay = gitWaitDelay
	out, err := c.CombinedOutput()
	if err != nil {
		return &SetupError{Cmd: cmd, Tree: tree, Output: string(out), Err: err}
	}
	return nil
}

// adoptable reports the registry entry for a healthy existing tree at path, nil
// when there is nothing there to adopt, and an error when the path is occupied by
// something that is not a worktree — which is a decision for a human, never a
// directory for trau to delete.
func adoptable(ctx context.Context, repoRoot, path string) (*Entry, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect worktree path %s: %w", path, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("worktree path %s is a file — move it aside", path)
	}
	if !folderrepo.IsRepo(path) {
		if empty, eerr := isEmptyDir(path); eerr == nil && empty {
			return nil, os.Remove(path)
		}
		return nil, fmt.Errorf("worktree path %s exists but is not a git working tree — move or remove it", path)
	}
	entries, err := List(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if samePath(e.Path, path) {
			return &e, nil
		}
	}
	return nil, fmt.Errorf("worktree path %s holds a git working tree %s does not own — move or remove it", path, repoRoot)
}

// startPoint is the commit a detached fresh tree begins at: the base's
// remote-tracking tip when it resolves, the local base otherwise, and HEAD when
// neither does — enough for `git worktree add` to succeed, since resolveBuildBranch
// cuts the ticket's branch from the right base inside the tree afterwards.
func startPoint(ctx context.Context, repoRoot, remote, base string) string {
	if base == "" {
		return "HEAD"
	}
	if remote != "" {
		if ref := remote + "/" + base; resolves(ctx, repoRoot, ref) {
			return ref
		}
	}
	if resolves(ctx, repoRoot, base) {
		return base
	}
	return "HEAD"
}

func resolves(ctx context.Context, repoRoot, rev string) bool {
	return run(ctx, repoRoot, "rev-parse", "--verify", "--quiet", rev+"^{commit}") == nil
}

func branchExists(ctx context.Context, repoRoot, branch string) bool {
	return run(ctx, repoRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch) == nil
}

// ignored reports whether git ignores rel at root. Only ignored files may be
// copied by glob: anything tracked is already in the tree at the commit it holds.
func ignored(ctx context.Context, root, rel string) bool {
	return run(ctx, root, "check-ignore", "--quiet", "--", rel) == nil
}

// untracked reports whether rel holds nothing git tracks at root.
func untracked(ctx context.Context, root, rel string) bool {
	out, err := output(ctx, root, "ls-files", "--", rel)
	return err == nil && strings.TrimSpace(out) == ""
}

// copyPath copies a file, a symlink's target content, or a whole directory tree,
// skipping any path in skip. A source that is not there is not an error: the copy
// set is a wish list, and a repo without a .env simply has nothing to bring.
func copyPath(src, dst string, skip map[string]bool) error {
	if skip[filepath.Clean(src)] {
		return nil
	}
	info, err := os.Lstat(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("copy %s into the worktree: %w", src, err)
	}
	if info.IsDir() {
		return copyDir(src, dst, skip)
	}
	// A symlink is followed rather than recreated: one pointing into the registered
	// root would make the tree read the very files it is isolated from.
	return copyFile(src, dst)
}

func copyDir(src, dst string, skip map[string]bool) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("copy %s into the worktree: %w", src, err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	var errs []error
	for _, e := range entries {
		if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), skip); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("copy %s into the worktree: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("copy %s into the worktree: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		return errors.Join(fmt.Errorf("write %s: %w", dst, err), out.Close())
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// inside reports whether path sits at or under root.
func inside(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// samePath reports whether two paths name the same directory. git's worktree list
// answers with symlinks resolved — on macOS every path under /var is really under
// /private/var — while trau's own paths come from configuration verbatim, so a
// plain string compare would take a tree it just created for a stranger's.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, aerr := filepath.EvalSymlinks(a)
	rb, berr := filepath.EvalSymlinks(b)
	return aerr == nil && berr == nil && filepath.Clean(ra) == filepath.Clean(rb)
}

func isEmptyDir(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func (o Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

func run(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.WaitDelay = gitWaitDelay
	if out, err := cmd.CombinedOutput(); err != nil {
		if detail := strings.TrimSpace(string(out)); detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}

func output(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.WaitDelay = gitWaitDelay
	out, err := cmd.Output()
	return string(out), err
}
