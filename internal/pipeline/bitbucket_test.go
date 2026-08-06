package pipeline

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/forge"
	"github.com/RomkaLTU/trau/internal/forge/bitbucketapi"
)

func bitbucketAt(t *testing.T, h http.HandlerFunc) Bitbucket {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return Bitbucket{Client: bitbucketapi.New("acme", "widgets", "rd@acme.com", "tok").WithBaseURL(ts.URL)}
}

// TestBitbucketSatisfiesDeliveryButNotStacker is the contract ADR 0036 draws:
// every forge answers the pull-request seam, and only GitHub stacks.
func TestBitbucketSatisfiesDeliveryButNotStacker(t *testing.T) {
	var d Delivery = Bitbucket{}
	if _, ok := d.(Stacker); ok {
		t.Error("Bitbucket implements Stacker, but Bitbucket has no stacked pull requests")
	}
	var gh Delivery = ExecGitHub{}
	if _, ok := gh.(Stacker); !ok {
		t.Error("ExecGitHub does not implement Stacker, but stacked epics ship through it")
	}
}

// TestBitbucketPRURLNeverAdoptsADeadPullRequest guards the trap Bitbucket sets
// that GitHub does not: it keeps every declined PR a branch ever had.
func TestBitbucketPRURLNeverAdoptsADeadPullRequest(t *testing.T) {
	b := bitbucketAt(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"values":[{"id":4,"state":"DECLINED","links":{"html":{"href":"https://bitbucket.org/acme/widgets/pull-requests/4"}}}]}`))
	})
	url, err := b.PRURL(context.Background(), "feature/COD-1")
	if err != nil || url != "" {
		t.Errorf("PRURL = (%q, %v), want no PR adopted", url, err)
	}
}

func TestBitbucketMergedPRURLAdoptsAMergedPullRequest(t *testing.T) {
	b := bitbucketAt(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"values":[{"id":4,"state":"MERGED","links":{"html":{"href":"https://bitbucket.org/acme/widgets/pull-requests/4"}}}]}`))
	})
	url, err := b.MergedPRURL(context.Background(), "epic/COD-1-thing")
	if err != nil || url != "https://bitbucket.org/acme/widgets/pull-requests/4" {
		t.Errorf("MergedPRURL = (%q, %v), want the merged PR's URL", url, err)
	}
}

// TestBitbucketReadsSwallowFailures keeps the swallow-and-default convention the
// phases depend on: a transient API failure re-polls instead of failing a ticket.
func TestBitbucketReadsSwallowFailures(t *testing.T) {
	b := bitbucketAt(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	ctx := context.Background()
	if url, err := b.PRURL(ctx, "feature/COD-1"); url != "" || err != nil {
		t.Errorf("PRURL = (%q, %v), want ('', nil)", url, err)
	}
	if st, err := b.PRState(ctx, "12"); st != "" || err != nil {
		t.Errorf("PRState = (%q, %v), want ('', nil)", st, err)
	}
	if checks, err := b.Checks(ctx, "12"); len(checks) != 0 || err != nil {
		t.Errorf("Checks = (%v, %v), want (none, nil)", checks, err)
	}
	if c, f, err := b.PRSize(ctx, "12"); c != 0 || f != 0 || err != nil {
		t.Errorf("PRSize = (%d, %d, %v), want (0, 0, nil)", c, f, err)
	}
}

// TestBitbucketPRStateReportsADeclinedPRAsClosed keeps the state vocabulary one:
// the phases give up on a PR a human rejected, which Bitbucket calls DECLINED and
// every phase branches on as CLOSED.
func TestBitbucketPRStateReportsADeclinedPRAsClosed(t *testing.T) {
	b := bitbucketAt(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":12,"state":"DECLINED"}`))
	})
	if st, err := b.PRState(context.Background(), "12"); st != "CLOSED" || err != nil {
		t.Errorf("PRState = (%q, %v), want (\"CLOSED\", nil)", st, err)
	}
}

// TestBitbucketChecksRollUpIntoTheGatesVocabulary proves the merge gate reads one
// set of words whichever forge produced them.
func TestBitbucketChecksRollUpIntoTheGatesVocabulary(t *testing.T) {
	b := bitbucketAt(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"values":[
			{"key":"build","name":"Pipeline #12","state":"SUCCESSFUL"},
			{"key":"lint","name":"Lint","state":"INPROGRESS"}
		]}`))
	})
	checks, err := b.Checks(context.Background(), "12")
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if evalChecks(checks, nil) != ciWaiting {
		t.Errorf("evalChecks = %v, want the gate still waiting on the in-progress check", evalChecks(checks, nil))
	}
	if len(checks) != 2 || checks[0].Bucket != "pass" || checks[1].Bucket != "pending" {
		t.Errorf("checks = %+v, want pass then pending", checks)
	}
}

func TestBitbucketMergeSendsTheMappedStrategy(t *testing.T) {
	var got string
	b := bitbucketAt(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got = string(raw)
		_, _ = w.Write([]byte(`{}`))
	})
	if err := b.Merge(context.Background(), "12", "squash", true); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !strings.Contains(got, `"merge_strategy":"squash"`) || !strings.Contains(got, `"close_source_branch":true`) {
		t.Errorf("merge body = %s, want the squash strategy and a branch delete", got)
	}
}

// TestBitbucketNotReadyNamesTheMissingHalf keeps the pre-spend refusal actionable:
// the run states what is missing before a ticket is picked, never at PR time.
func TestBitbucketNotReadyNamesTheMissingHalf(t *testing.T) {
	cases := []struct {
		name   string
		client Bitbucket
		want   string
	}{
		{"no client", Bitbucket{}, "no Bitbucket client"},
		{"no slug", Bitbucket{Client: bitbucketapi.New("", "", "rd@acme.com", "tok")}, "names no workspace"},
		{"no credentials", Bitbucket{Client: bitbucketapi.New("acme", "widgets", "", "")}, "BITBUCKET_EMAIL"},
		{"ready", Bitbucket{Client: bitbucketapi.New("acme", "widgets", "rd@acme.com", "tok")}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.client.NotReady()
			if tc.want == "" {
				if got != "" {
					t.Errorf("NotReady = %q, want it ready", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("NotReady = %q, want it to name %q", got, tc.want)
			}
		})
	}
}

// TestNewDeliveryPicksTheBackendFromTheDeclaredForge covers the selection without
// a git remote: the declared FORGE overrides detection everywhere else, and it
// does here too.
func TestNewDeliveryPicksTheBackendFromTheDeclaredForge(t *testing.T) {
	creds := BitbucketCredentials{Email: "rd@acme.com", APIToken: "tok"}
	if _, ok := NewDelivery(context.Background(), t.TempDir(), string(forge.Bitbucket), "origin", creds).(Bitbucket); !ok {
		t.Error("a declared bitbucket forge did not select the Bitbucket backend")
	}
	if _, ok := NewDelivery(context.Background(), t.TempDir(), string(forge.GitHub), "origin", creds).(ExecGitHub); !ok {
		t.Error("a declared github forge did not select the gh backend")
	}
	if _, ok := NewDelivery(context.Background(), t.TempDir(), "", "origin", creds).(ExecGitHub); !ok {
		t.Error("a repo with no remote did not fall back to the gh backend")
	}
}
