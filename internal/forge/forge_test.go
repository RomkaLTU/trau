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

// TestUnsupportedNamesEveryForgeWithNoDeliveryPath guards the gate a run spends
// nothing before: GitHub, Bitbucket and a remote-less repo pass (ADR 0036), and
// each refusal says why.
func TestUnsupportedNamesEveryForgeWithNoDeliveryPath(t *testing.T) {
	for _, f := range []Forge{GitHub, Bitbucket, None} {
		if reason := f.Unsupported(); reason != "" {
			t.Errorf("%q refused with %q, want it deliverable", f, reason)
		}
	}
	for _, f := range []Forge{Azure, GitLab, Unknown} {
		if f.Unsupported() == "" {
			t.Errorf("%q reads as deliverable, but trau opens no pull requests there", f)
		}
	}
}

// TestSlugReadsOwnerAndRepoFromEveryRemoteForm proves the identifier both forges'
// REST APIs address a repository by is read off the remote alone, before any
// credential exists.
func TestSlugReadsOwnerAndRepoFromEveryRemoteForm(t *testing.T) {
	cases := []struct {
		name       string
		remote     string
		wantOwner  string
		wantRepo   string
		wantParsed bool
	}{
		{"bitbucket https", "https://bitbucket.org/acme/widgets.git", "acme", "widgets", true},
		{"bitbucket https with user", "https://rd@bitbucket.org/acme/widgets.git", "acme", "widgets", true},
		{"bitbucket scp", "git@bitbucket.org:acme/widgets.git", "acme", "widgets", true},
		{"bitbucket no suffix", "https://bitbucket.org/acme/widgets", "acme", "widgets", true},
		{"github scp", "git@github.com:RomkaLTU/trau.git", "RomkaLTU", "trau", true},
		{"azure deep path", "https://dev.azure.com/org/project/_git/repo", "_git", "repo", true},
		{"local path", "/Users/rd/Projects/loop", "", "", false},
		{"host only", "https://bitbucket.org/acme", "", "", false},
		{"empty", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, ok := Slug(tc.remote)
			if ok != tc.wantParsed || owner != tc.wantOwner || repo != tc.wantRepo {
				t.Errorf("Slug(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.remote, owner, repo, ok, tc.wantOwner, tc.wantRepo, tc.wantParsed)
			}
		})
	}
}
