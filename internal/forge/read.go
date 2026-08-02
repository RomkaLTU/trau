package forge

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// lsRemoteBudget bounds the one call in this package that leaves the machine.
// Asking the remote is the only honest answer for the many working copies git
// never wrote refs/remotes/<remote>/HEAD into, but a folder holds around fifty
// repositories and an unreachable host must not hold a wizard or a run open.
const lsRemoteBudget = 10 * time.Second

// RemoteURL is the URL dir has configured for remote, or "" when it has none.
// Best-effort: a directory without git, or without that remote, reads as "".
func RemoteURL(ctx context.Context, dir, remote string) string {
	return gitOutput(ctx, dir, "remote", "get-url", remoteOr(remote))
}

// DefaultBranch is the branch remote itself calls default. refs/remotes/<remote>/HEAD
// is the record git writes at clone time and reading it is a lookup, not a
// network call; the working copies missing it — plenty of them — cost one
// ls-remote instead. What is never consulted is the branch dir happens to have
// checked out: a repository parked on a feature branch is still a repository
// whose default is master, and reporting the parked branch as the default is how
// that value ends up written into config as a base to build off.
func DefaultBranch(ctx context.Context, dir, remote string) string {
	remote = remoteOr(remote)
	if head := gitOutput(ctx, dir, "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD"); head != "" {
		return strings.TrimPrefix(head, remote+"/")
	}
	ctx, cancel := context.WithTimeout(ctx, lsRemoteBudget)
	defer cancel()
	return symrefBranch(gitOutput(ctx, dir, "ls-remote", "--symref", remote, "HEAD"))
}

// symrefBranch reads the branch out of the "ref: refs/heads/master\tHEAD" line
// ls-remote --symref leads with. An empty remote, or one that answered with a
// bare sha, has no such line.
func symrefBranch(out string) string {
	line, _, _ := strings.Cut(out, "\n")
	ref, _, ok := strings.Cut(strings.TrimPrefix(line, "ref: "), "\t")
	if !ok || !strings.HasPrefix(ref, "refs/heads/") {
		return ""
	}
	return strings.TrimPrefix(ref, "refs/heads/")
}

// remoteOr defaults an unset remote name to origin, the same fallback the rest
// of trau applies to an empty REMOTE.
func remoteOr(remote string) string {
	if remote == "" {
		return "origin"
	}
	return remote
}

func gitOutput(ctx context.Context, dir string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// A remote whose credentials this machine does not hold must fail rather than
	// park the caller on a hidden credential prompt.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
