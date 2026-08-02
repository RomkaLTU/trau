package forge

import "testing"

// TestFromRemoteIdentifiesTheHost is the whole point of the package: which forge a
// repository is on comes off that repository's own remote URL, in every form git
// accepts it, and an unknown host is named unknown rather than assumed to be the
// one trau can deliver to.
func TestFromRemoteIdentifiesTheHost(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		want   Forge
	}{
		{"scp-style github", "git@github.com:acme/api.git", GitHub},
		{"https github", "https://github.com/acme/api.git", GitHub},
		{"https azure devops", "https://acme@dev.azure.com/acme/platform/_git/api", Azure},
		{"ssh azure devops", "ssh://git@ssh.dev.azure.com/v3/acme/platform/api", Azure},
		{"legacy visualstudio host", "https://acme.visualstudio.com/platform/_git/api", Azure},
		{"gitlab", "git@gitlab.com:acme/api.git", GitLab},
		{"self-hosted host nobody can enumerate", "git@git.acme.internal:acme/api.git", Unknown},
		{"local path remote", "/srv/git/api.git", None},
		{"windows path remote", `C:\repos\api.git`, None},
		{"no remote at all", "", None},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FromRemote(tc.remote); got != tc.want {
				t.Errorf("FromRemote(%q) = %q, want %q", tc.remote, got, tc.want)
			}
		})
	}
}

// TestResolvePrefersWhatTheRepoDeclares covers the escape hatch a self-hosted
// install needs: the repository's own FORGE key overrides its remote, and a value
// naming no forge leaves the remote's answer standing rather than a typo's.
func TestResolvePrefersWhatTheRepoDeclares(t *testing.T) {
	const remote = "git@git.acme.internal:acme/api.git"
	if got := Resolve("github", remote); got != GitHub {
		t.Errorf("declared forge = %q, want %q", got, GitHub)
	}
	if got := Resolve("githbu", remote); got != Unknown {
		t.Errorf("misspelled forge = %q, want the remote's own answer %q", got, Unknown)
	}
}

// TestResolveBasePrefersTheRemoteOverTheFallback is the per-child base contract: a
// child whose remote calls master default ships to master even inside a folder
// configured for main, and only a remote with no answer falls back.
func TestResolveBasePrefersTheRemoteOverTheFallback(t *testing.T) {
	if got := ResolveBase("", "master", "main"); got != "master" {
		t.Errorf("base = %q, want the branch the remote calls default", got)
	}
	if got := ResolveBase("release", "master", "main"); got != "release" {
		t.Errorf("base = %q, want what the repo declares for itself", got)
	}
	if got := ResolveBase("", "", "main"); got != "main" {
		t.Errorf("base = %q, want the fallback when the remote has no answer", got)
	}
}

// TestUnsupportedNamesEveryForgeButGitHub guards the gate a run spends nothing
// before: only GitHub and a remote-less repo pass, and each refusal says why.
func TestUnsupportedNamesEveryForgeButGitHub(t *testing.T) {
	for _, f := range []Forge{GitHub, None} {
		if reason := f.Unsupported(); reason != "" {
			t.Errorf("%q refused with %q, want it deliverable", f, reason)
		}
	}
	for _, f := range []Forge{Azure, GitLab, Bitbucket, Unknown} {
		if f.Unsupported() == "" {
			t.Errorf("%q reads as deliverable, but trau opens pull requests on GitHub only", f)
		}
	}
}
