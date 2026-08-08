package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoConfigFile is the repo-pinned git config at the target repo's root
// (identity and other per-repo git settings). When it exists, trau wires it
// into the repo's local git config via include.path so every git commit made
// in the repo — trau's own and each agent subprocess's — resolves from the
// repo-pinned file instead of the developer's global ~/.gitconfig.
const RepoConfigFile = ".gitconfig.repo"

// repoConfigInclude is the include.path value written to the repo's local git
// config. Kept relative so the repo can move on disk: git resolves it against
// the config file containing the directive (<repo>/.git/config → <repo>/.gitconfig.repo).
const repoConfigInclude = "../" + RepoConfigFile

// folderConfigInclude reaches a Folder repo root's .gitconfig.repo from one of
// its children (<child>/.git/config → <root>/.gitconfig.repo), one level further
// up than a repo's own.
const folderConfigInclude = "../" + repoConfigInclude

// EnsureRepoConfigInclude wires <repoRoot>/.gitconfig.repo into the repo's
// local git config as an include.path entry. No-op when the file is absent or
// the include is already present. Returns whether it added the include. A
// present file that cannot be wired is an error — proceeding would risk
// commits under a prohibited identity.
//
// workTree is the tree the run acts in. A linked worktree writes to the main
// checkout's shared .git/config, so wiring is once-is-enough for every tree of
// the repo; the include value is measured from the common git directory so it
// still reaches the registered root's file. An empty workTree, or one equal to
// repoRoot, is the ordinary single-checkout path.
func EnsureRepoConfigInclude(ctx context.Context, repoRoot, workTree string) (bool, error) {
	if repoRoot == "" {
		return false, nil
	}
	configFile := filepath.Join(repoRoot, RepoConfigFile)
	if workTree == "" || workTree == repoRoot {
		return ensureConfigInclude(ctx, repoRoot, configFile, repoConfigInclude)
	}
	if _, err := os.Stat(configFile); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	include, err := worktreeIncludePath(ctx, workTree, configFile)
	if err != nil {
		return false, err
	}
	return ensureConfigIncludeIn(ctx, workTree, configFile, include, true)
}

// worktreeIncludePath is the include.path value that reaches configFile from the
// git config file the worktree writes with `config --local`. A linked worktree
// shares that file with the main checkout, so the value is resolved against the
// common git directory rather than the worktree's own .git pointer file. For a
// checkout that is its own main tree this returns the ordinary "../.gitconfig.repo".
func worktreeIncludePath(ctx context.Context, workTree, configFile string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", workTree, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", fmt.Errorf("resolve git common dir for %s: %w", workTree, err)
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(workTree, common)
	}
	// git answers with the path it recorded when the worktree was added, which may
	// have travelled through a symlink the caller's own path did not (macOS /var).
	// Both sides are resolved so the relative value reads the same however the run
	// spelled its roots — and so it matches the constant a plain checkout writes.
	rel, err := filepath.Rel(realPath(filepath.Clean(common)), realPath(configFile))
	if err != nil {
		return "", fmt.Errorf("locate %s from %s: %w", RepoConfigFile, common, err)
	}
	return filepath.ToSlash(rel), nil
}

// realPath resolves path through any symlinks, falling back to the path as given
// when it cannot be resolved.
func realPath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

// worktreeConfigEnabled reports whether the repository splits per-worktree
// settings into $GIT_DIR/config.worktree (extensions.worktreeConfig). It decides
// only where an already-wired include may be found — the write itself always goes
// to the shared local config. An unreadable or unset key reads as off.
func worktreeConfigEnabled(ctx context.Context, gitDir string) bool {
	out, err := exec.CommandContext(ctx, "git", "-C", gitDir, "config", "--bool", "--get", "extensions.worktreeConfig").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// EnsureChildConfigInclude wires the identity a Folder repo's child commits
// under: the child's own .gitconfig.repo when it has one, the folder root's
// otherwise — the same child-overrides-folder grain as .trau/checks. The folder
// root itself has no git config to wire it into, so this is where a folder run's
// identity is pinned, in the children the ticket actually reached.
func EnsureChildConfigInclude(ctx context.Context, root, child string) (bool, error) {
	own := filepath.Join(child, RepoConfigFile)
	if _, err := os.Stat(own); err == nil {
		return ensureConfigInclude(ctx, child, own, repoConfigInclude)
	}
	return ensureConfigInclude(ctx, child, filepath.Join(root, RepoConfigFile), folderConfigInclude)
}

func ensureConfigInclude(ctx context.Context, repoRoot, configFile, include string) (bool, error) {
	return ensureConfigIncludeIn(ctx, repoRoot, configFile, include, false)
}

// ensureConfigIncludeIn wires include into the local git config of the tree at
// dir. linked marks dir as a linked worktree, where an include the repository
// already pinned may sit in the per-worktree config instead of the shared one.
func ensureConfigIncludeIn(ctx context.Context, dir, configFile, include string, linked bool) (bool, error) {
	if _, err := os.Stat(configFile); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", RepoConfigFile, err)
	}
	g := ExecGit{Repo: dir}
	// Read the file before including it: an include.path git cannot parse breaks
	// every later git call in the repo, down to the unset that would remove it
	// again, so a malformed file has to be refused rather than wired and undone.
	if err := g.run(ctx, "config", "--file", configFile, "--list"); err != nil {
		return false, fmt.Errorf("read %s: %w", RepoConfigFile, err)
	}
	scopes := []string{"--local"}
	if linked && worktreeConfigEnabled(ctx, dir) {
		scopes = append(scopes, "--worktree")
	}
	for _, scope := range scopes {
		out, err := exec.CommandContext(ctx, "git", "-C", dir, "config", scope, "--get-all", "include.path").Output()
		if err != nil {
			// Exit status 1 means the key is simply unset; anything else is real.
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				return false, fmt.Errorf("read include.path: %w", err)
			}
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.TrimSpace(line) == include {
				return false, nil
			}
		}
	}
	if err := g.run(ctx, "config", "--local", "--add", "include.path", include); err != nil {
		return false, err
	}
	return true, nil
}
