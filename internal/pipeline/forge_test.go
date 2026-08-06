package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/forge"
	"github.com/RomkaLTU/trau/internal/forge/bitbucketapi"
)

// bitbucketGit is a fakeGit whose remote is on bitbucket.org, so the run resolves
// a forge that now delivers rather than one it refuses.
type bitbucketGit struct{ fakeGit }

func (bitbucketGit) RemoteURL(context.Context, string) string {
	return "git@bitbucket.org:acme/widgets.git"
}

// gitlabGit is a fakeGit on a forge trau still opens no pull requests on.
type gitlabGit struct{ fakeGit }

func (gitlabGit) RemoteURL(context.Context, string) string {
	return "git@gitlab.com:acme/widgets.git"
}

// TestAssertDeliverableRefusesBeforeAnythingIsSpent is the gate ADR 0036 keeps:
// an unsupported host and a supported host with no credential are both named
// before a ticket is picked, never at PR time.
func TestAssertDeliverableRefusesBeforeAnythingIsSpent(t *testing.T) {
	cases := []struct {
		name     string
		git      Git
		delivery Delivery
		want     string
	}{
		{
			name:     "github with gh",
			git:      fakeGit{},
			delivery: ExecGitHub{},
		},
		{
			name:     "bitbucket with credentials",
			git:      bitbucketGit{},
			delivery: Bitbucket{Client: bitbucketapi.New("acme", "widgets", "rd@acme.com", "tok")},
		},
		{
			name:     "bitbucket without credentials",
			git:      bitbucketGit{},
			delivery: Bitbucket{Client: bitbucketapi.New("acme", "widgets", "", "")},
			want:     "BITBUCKET_EMAIL",
		},
		{
			name:     "a forge with no delivery path",
			git:      gitlabGit{},
			delivery: ExecGitHub{},
			want:     "GitLab",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pipeline{Git: tc.git, Delivery: tc.delivery, Remote: "origin"}
			err := p.assertDeliverable(context.Background())
			if tc.want == "" {
				if err != nil {
					t.Fatalf("assertDeliverable = %v, want the run allowed", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("assertDeliverable = %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
}

// TestForgeOfReadsTheRemoteAndHonoursTheDeclaration keeps FORGE's precedence: a
// repository's own declaration wins, and the remote answers otherwise.
func TestForgeOfReadsTheRemoteAndHonoursTheDeclaration(t *testing.T) {
	p := &Pipeline{Remote: "origin"}
	if got := p.forgeOf(context.Background(), bitbucketGit{}, ""); got != forge.Bitbucket {
		t.Errorf("forgeOf = %q, want it read off the bitbucket.org remote", got)
	}
	if got := p.forgeOf(context.Background(), bitbucketGit{}, "github"); got != forge.GitHub {
		t.Errorf("forgeOf = %q, want the declared forge to win", got)
	}
}
