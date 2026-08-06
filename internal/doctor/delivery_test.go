package doctor

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/forge"
)

// repoWithRemote is a git repository whose origin points at url, which is all
// the forge readiness checks read.
func repoWithRemote(t *testing.T, url string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", url},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return dir
}

// TestDeliveryForgesNamesOnlyTheForgesARunReaches is what stopped gh being a
// universal requirement: a repo answers for its own remote and for nothing else.
func TestDeliveryForgesNamesOnlyTheForgesARunReaches(t *testing.T) {
	cases := []struct {
		name      string
		remote    string
		declared  string
		want      []forge.Forge
		wantKnown bool
	}{
		{"github remote", "git@github.com:acme/widgets.git", "", []forge.Forge{forge.GitHub}, true},
		{"bitbucket remote", "git@bitbucket.org:acme/widgets.git", "", []forge.Forge{forge.Bitbucket}, true},
		{"declared forge wins", "git@bitbucket.org:acme/widgets.git", "github", []forge.Forge{forge.GitHub}, true},
		{"a forge with no delivery path", "git@gitlab.com:acme/widgets.git", "", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Remote: "origin", Forge: tc.declared}
			got, known := deliveryForges(context.Background(), cfg, repoWithRemote(t, tc.remote))
			if known != tc.wantKnown {
				t.Fatalf("known = %v, want %v", known, tc.wantKnown)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("forges = %+v, want %v", got, tc.want)
			}
			for i, f := range tc.want {
				if got[i].forge != f {
					t.Errorf("forge[%d] = %q, want %q", i, got[i].forge, f)
				}
			}
		})
	}
}

// TestDeliveryForgesReportsLocalDeliveryAsNoForge keeps a remote-less repo from
// being asked for a credential it never uses.
func TestDeliveryForgesReportsLocalDeliveryAsNoForge(t *testing.T) {
	got, known := deliveryForges(context.Background(), config.Config{Remote: "origin"}, t.TempDir())
	if !known || len(got) != 0 {
		t.Errorf("deliveryForges = (%+v, %v), want no forge and a known answer", got, known)
	}
	if _, known := deliveryForges(context.Background(), config.Config{Remote: "origin"}, ""); known {
		t.Error("deliveryForges outside a repo reported a known answer, want unknown")
	}
}

// TestCheckDeliveryAsksNoGitHubOfABitbucketRepo is the readiness half of ADR
// 0036: only the forge a run actually reaches is checked.
func TestCheckDeliveryAsksNoGitHubOfABitbucketRepo(t *testing.T) {
	rr := newTestRunner()
	root := repoWithRemote(t, "git@bitbucket.org:acme/widgets.git")
	checkDelivery(context.Background(), config.Config{Remote: "origin"}, root, rr)

	c := lastCheck(t, rr)
	if c.Name != "bitbucket" {
		t.Fatalf("check = %q, want the bitbucket credential check and no gh check", c.Name)
	}
	if c.Status != fail || !strings.Contains(c.Message, "BITBUCKET_API_TOKEN") {
		t.Errorf("check = %+v, want a failure naming the missing keys", c)
	}
	for _, got := range rr.r.Checks {
		if got.Name == "gh" {
			t.Errorf("a bitbucket repo was asked for gh: %+v", got)
		}
	}
}

// TestCheckDeliveryAsksNothingOfALocalRepo: with no remote a run squash-merges
// locally and reaches no forge at all.
func TestCheckDeliveryAsksNothingOfALocalRepo(t *testing.T) {
	rr := newTestRunner()
	checkDelivery(context.Background(), config.Config{Remote: "origin"}, t.TempDir(), rr)
	if len(rr.r.Checks) != 0 {
		t.Errorf("checks = %+v, want none for a repo with no remote", rr.r.Checks)
	}
}

// TestCheckBitbucketWarnsOnARemoteWithNoRepository stops the check short of a
// call that can only fail, and keeps its message free of token material.
func TestCheckBitbucketWarnsOnARemoteWithNoRepository(t *testing.T) {
	const token = "s3cr3t-token"
	rr := newTestRunner()
	root := repoWithRemote(t, "https://bitbucket.org/acme")
	checkBitbucket(context.Background(), config.Config{
		Remote: "origin", BitbucketEmail: "rd@acme.com", BitbucketAPIToken: token,
	}, root, rr)

	c := lastCheck(t, rr)
	if c.Status != warn || !strings.Contains(c.Message, "names no workspace and repository") {
		t.Errorf("check = %+v, want a warning about the remote's shape", c)
	}
	if strings.Contains(c.Message, token) {
		t.Errorf("check message leaked the token: %q", c.Message)
	}
}
