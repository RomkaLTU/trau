package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/prompts"
)

func TestQARosterNoteEmptyWhenNothingToInject(t *testing.T) {
	if got := qaRosterNote(nil, ""); got != "" {
		t.Errorf("qaRosterNote(nil, \"\") = %q, want empty", got)
	}
	if got := qaRosterNote([]hubclient.QAAccount{{Label: "  "}}, "   "); got != "" {
		t.Errorf("qaRosterNote(blank label, blank notes) = %q, want empty", got)
	}
}

func TestQARosterNoteCarriesCredentialsAndNoCopyOrder(t *testing.T) {
	note := qaRosterNote([]hubclient.QAAccount{
		{Label: "admin", Username: "admin@example.test", Secret: "s3cret", Description: "billing flows"},
	}, "login at /auth")

	for _, want := range []string{"admin", "admin@example.test", "s3cret", "billing flows", "login at /auth"} {
		if !strings.Contains(note, want) {
			t.Errorf("roster note missing %q: %s", want, note)
		}
	}
	for _, want := range []string{"NEVER", "verdict", "PR", "tracker"} {
		if !strings.Contains(note, want) {
			t.Errorf("roster note missing the no-copy instruction token %q", want)
		}
	}
}

func TestQARosterNoteNotesOnly(t *testing.T) {
	note := qaRosterNote(nil, "create a disposable admin via the seeder; delete it after")
	if !strings.Contains(note, "create a disposable admin") {
		t.Errorf("notes-only roster note dropped the notes: %s", note)
	}
	if strings.Contains(note, "Available accounts") {
		t.Errorf("notes-only roster note should list no accounts: %s", note)
	}
}

func TestQAVerifyNoteInjectsOnlyForBrowserUISlice(t *testing.T) {
	roster := hubclient.QARoster{
		Accounts: []hubclient.QAAccount{{Label: "admin", Username: "u", Secret: "p"}},
		Notes:    "notes",
	}
	fetch := func(context.Context) (hubclient.QARoster, error) { return roster, nil }

	cases := []struct {
		name    string
		files   []string
		note    string
		fetch   func(context.Context) (hubclient.QARoster, error)
		wantHit bool
	}{
		{"ui slice with browser note", uiFiles, "drive the app", fetch, true},
		{"backend slice", backendFiles, "drive the app", fetch, false},
		{"ui slice without a browser note", uiFiles, "", fetch, false},
		{"ui slice with no fetcher", uiFiles, "drive the app", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.Git = filesGit{files: tc.files}
			p.FetchQAAccounts = tc.fetch

			got := p.qaVerifyNote(context.Background(), tc.note)
			if hit := got != ""; hit != tc.wantHit {
				t.Fatalf("qaVerifyNote hit=%v (%q), want %v", hit, got, tc.wantHit)
			}
		})
	}
}

// TestQACredentialsConfinedToVerifyPrompt is the happy-path guarantee that a
// stored secret can only ever reach the verifier: it lands in the verify prompt
// but the handoff (QA-brief authoring) prompt has no channel for it, so the brief
// the verifier grades against can never carry a credential.
func TestQACredentialsConfinedToVerifyPrompt(t *testing.T) {
	const secret = "top-secret-pw"
	qaNote := qaRosterNote([]hubclient.QAAccount{{Label: "admin", Username: "u", Secret: secret}}, "")

	verify := verifyTail(prompts.Renderer{}, "COD-1", handoffPath("COD-1"), verifyPath("COD-1"), "drive the app", qaNote, "", "", "", "", "")
	if !strings.Contains(verify, secret) {
		t.Fatal("verify prompt should carry the injected credentials")
	}

	brief := handoffTail(prompts.Renderer{}, "COD-1", "")
	if strings.Contains(brief, secret) {
		t.Fatal("handoff/brief prompt leaked QA credentials")
	}
}
