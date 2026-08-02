// Package forge reads what a repository's own remote says about it: which code
// host would receive a push and open a pull request, and which branch that host
// calls default. Both are facts about that one repository's remote and about
// nothing else — a team's tickets may live on one service while its code lives
// on another, and a folder of repositories may legitimately spread its children
// across several forges.
package forge

import "strings"

// Forge is the code host a repository's remote points at.
type Forge string

const (
	GitHub    Forge = "github"
	Azure     Forge = "azure"
	GitLab    Forge = "gitlab"
	Bitbucket Forge = "bitbucket"

	// Unknown is a remote on a host this build does not recognize. It is
	// deliberately not a guess at GitHub: an operator names it with FORGE, and
	// until they do a run leaves it alone rather than dying at `gh pr create`
	// with the build agent already paid for.
	Unknown Forge = "unknown"

	// None is a repository with no remote configured, or one whose remote is a
	// path on this machine. Either way there is no forge to deliver through and
	// the run squash-merges locally instead.
	None Forge = ""
)

// hosts names the forge each hostname belongs to. Self-hosted installs answer on
// hostnames nobody can enumerate, which is what the FORGE key is for.
var hosts = map[string]Forge{
	"github.com":              GitHub,
	"ssh.github.com":          GitHub,
	"gitlab.com":              GitLab,
	"altssh.gitlab.com":       GitLab,
	"bitbucket.org":           Bitbucket,
	"altssh.bitbucket.org":    Bitbucket,
	"dev.azure.com":           Azure,
	"ssh.dev.azure.com":       Azure,
	"vs-ssh.visualstudio.com": Azure,
}

// Names lists the forges an operator may declare with the FORGE key.
func Names() []string {
	return []string{string(GitHub), string(Azure), string(GitLab), string(Bitbucket)}
}

// Parse reads a declared FORGE value. A value naming no forge this build knows
// is not one, which leaves the caller on detection rather than on a typo's word.
func Parse(declared string) (Forge, bool) {
	switch f := Forge(strings.ToLower(strings.TrimSpace(declared))); f {
	case GitHub, Azure, GitLab, Bitbucket:
		return f, true
	}
	return None, false
}

// Resolve settles where a repository is hosted: what that repository declares
// for itself wins — the only way to name a self-hosted install or a forge this
// build does not know by host — and otherwise it is read off its own remote.
// Nothing is inferred from the tracker: where a team's tickets live says nothing
// about where its code does.
func Resolve(declared, remoteURL string) Forge {
	if f, ok := Parse(declared); ok {
		return f
	}
	return FromRemote(remoteURL)
}

// ResolveBase settles the branch a repository ships to: the BASE_BRANCH it
// declares for itself, else the branch its own remote calls default, else the
// fallback its folder or config supplies. A folder's children have as many bases
// as it has repositories, and forcing one on all of them leaves the odd one out
// permanently unshippable.
func ResolveBase(declared, remoteDefault, fallback string) string {
	for _, branch := range []string{declared, remoteDefault, fallback} {
		if branch = strings.TrimSpace(branch); branch != "" {
			return branch
		}
	}
	return ""
}

// FromRemote identifies the forge hosting a git remote URL.
func FromRemote(remoteURL string) Forge {
	host := Host(remoteURL)
	switch {
	case host == "":
		return None
	case strings.HasSuffix(host, ".visualstudio.com"):
		return Azure
	}
	if f, ok := hosts[host]; ok {
		return f
	}
	return Unknown
}

// Host is the hostname a git remote URL names, across the URL, scp-style and
// local-path forms git accepts. A remote naming no host — a path on this machine,
// which is how an air-gapped checkout and most of the test suite deliver — is "".
func Host(remoteURL string) string {
	remoteURL = strings.TrimSpace(remoteURL)
	authority := ""
	switch scheme, rest, hasScheme := strings.Cut(remoteURL, "://"); {
	case hasScheme && scheme == "file":
		return ""
	case hasScheme:
		authority, _, _ = strings.Cut(rest, "/")
	default:
		// The scp-style form is user@host:path. Anything left is a filesystem
		// path — a drive letter's colon is not a host separator.
		var scp bool
		authority, _, scp = strings.Cut(remoteURL, ":")
		if !scp || strings.ContainsAny(authority, `/\`) || !strings.ContainsAny(authority, ".@") {
			return ""
		}
	}
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		authority = authority[at+1:]
	}
	host, _, _ := strings.Cut(authority, ":")
	return strings.ToLower(host)
}

// Label names the forge the way a report should say it out loud.
func (f Forge) Label() string {
	switch f {
	case GitHub:
		return "GitHub"
	case Azure:
		return "Azure DevOps"
	case GitLab:
		return "GitLab"
	case Bitbucket:
		return "Bitbucket"
	case Unknown:
		return "an unrecognized forge"
	}
	return "no remote"
}

// Unsupported names why a run cannot deliver to this forge, or "" when it can.
// Opening pull requests is GitHub-only, so this is a fact a run states before it
// spends anything rather than one it discovers at `gh pr create`, after the
// build, handoff and verify agents have been paid for.
func (f Forge) Unsupported() string {
	switch f {
	case GitHub, None:
		return ""
	case Unknown:
		return "its remote is on a forge trau does not recognize — name it with FORGE in its .trau.ini"
	}
	return "its remote is on " + f.Label() + ", and trau opens pull requests on GitHub only"
}
