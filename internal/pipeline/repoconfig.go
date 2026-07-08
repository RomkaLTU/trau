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

// EnsureRepoConfigInclude wires <repoRoot>/.gitconfig.repo into the repo's
// local git config as an include.path entry. No-op when the file is absent or
// the include is already present. Returns whether it added the include. A
// present file that cannot be wired is an error — proceeding would risk
// commits under a prohibited identity.
func EnsureRepoConfigInclude(ctx context.Context, repoRoot string) (bool, error) {
	if repoRoot == "" {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(repoRoot, RepoConfigFile)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", RepoConfigFile, err)
	}
	out, err := exec.CommandContext(ctx, "git", "-C", repoRoot, "config", "--local", "--get-all", "include.path").Output()
	if err != nil {
		// Exit status 1 means the key is simply unset; anything else is real.
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return false, fmt.Errorf("read include.path: %w", err)
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == repoConfigInclude {
			return false, nil
		}
	}
	if err := (ExecGit{Repo: repoRoot}).run(ctx, "config", "--local", "--add", "include.path", repoConfigInclude); err != nil {
		return false, err
	}
	return true, nil
}
