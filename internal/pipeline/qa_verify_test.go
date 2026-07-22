package pipeline

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/prompts"
)

// TestQARosterNoteEmptyRosterStillInstructs is the melga case: a repo with
// nothing stored is the one that has to discover its own credentials, so the
// fragment must still reach the verifier with the discovery and capture orders.
func TestQARosterNoteEmptyRosterStillInstructs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		accounts []hubclient.QAAccount
		notes    string
	}{
		{"nothing stored", nil, ""},
		{"blank label and notes", []hubclient.QAAccount{{Label: "  "}}, "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			note := qaRosterNote("COD-1", tc.accounts, tc.notes)
			if strings.Contains(note, "Available accounts") {
				t.Errorf("empty roster listed accounts: %s", note)
			}
			for _, want := range []string{"search the repo under test", qaCapturePath("COD-1")} {
				if !strings.Contains(note, want) {
					t.Errorf("empty-roster note missing %q: %s", want, note)
				}
			}
		})
	}
}

// TestQARosterNoteDiscoveryIsRepoScoped pins the boundary the discovery order
// draws: the repo under test is the only permitted source, and the capture file
// the only permitted destination.
func TestQARosterNoteDiscoveryIsRepoScoped(t *testing.T) {
	note := qaRosterNote("COD-1", nil, "")

	for _, want := range []string{
		"seed data",
		"fixtures",
		"environment-variable examples",
		"never reach for credentials in your own configuration files",
		"ONLY place a discovered credential value may be written",
		`{"accounts": [{"label": "...", "username": "...", "secret": "...", "description": "..."}]}`,
		"never the username",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("discovery note missing %q: %s", want, note)
		}
	}
}

func TestQARosterNoteCarriesCredentialsAndNoCopyOrder(t *testing.T) {
	note := qaRosterNote("COD-1", []hubclient.QAAccount{
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
	note := qaRosterNote("COD-1", nil, "create a disposable admin via the seeder; delete it after")
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
	empty := func(context.Context) (hubclient.QARoster, error) { return hubclient.QARoster{}, nil }
	unreachable := func(context.Context) (hubclient.QARoster, error) {
		return hubclient.QARoster{}, errors.New("hub down")
	}

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
		{"ui slice with an empty roster", uiFiles, "drive the app", empty, true},
		{"ui slice with an unreachable roster", uiFiles, "drive the app", unreachable, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.Git = filesGit{files: tc.files}
			p.FetchQAAccounts = tc.fetch

			got := p.qaVerifyNote(context.Background(), "COD-1", tc.note)
			if hit := got != ""; hit != tc.wantHit {
				t.Fatalf("qaVerifyNote hit=%v (%q), want %v", hit, got, tc.wantHit)
			}
		})
	}
}

// TestQAVerifyNoteReportsEveryOutcome is the observability guarantee: whenever
// the QA gate is active the run says what the roster contributed — injected,
// empty, or unreachable — through both the log and a counts-only event.
func TestQAVerifyNoteReportsEveryOutcome(t *testing.T) {
	cases := []struct {
		name         string
		fetch        func(context.Context) (hubclient.QARoster, error)
		wantLog      string
		wantAccounts float64
		wantNotes    bool
		wantErrField string
	}{
		{
			name: "injected",
			fetch: func(context.Context) (hubclient.QARoster, error) {
				return hubclient.QARoster{
					Accounts: []hubclient.QAAccount{
						{Label: "admin", Username: "admin@example.test", Secret: "s3cret"},
						{Label: "member", Username: "member@example.test", Secret: "hunter2"},
						{Username: "unlabeled@example.test"},
					},
					Notes: "sign in at /auth",
				}, nil
			},
			wantLog:      "QA roster injected: 2 account(s) + QA notes",
			wantAccounts: 2,
			wantNotes:    true,
		},
		{
			name: "empty",
			fetch: func(context.Context) (hubclient.QARoster, error) {
				return hubclient.QARoster{}, nil
			},
			wantLog: qaNoRosterWarning,
		},
		{
			name: "fetch failed",
			fetch: func(context.Context) (hubclient.QARoster, error) {
				return hubclient.QARoster{}, errors.New("hub down")
			},
			wantLog:      qaRosterUnavailableWarning,
			wantErrField: "hub down",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logs := &logRenderer{}
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.Git = filesGit{files: uiFiles}
			p.Events = event.New(&buf)
			p.Renderer = logs
			p.FetchQAAccounts = tc.fetch

			p.qaVerifyNote(context.Background(), "COD-1", "drive the app")

			if !logs.contains(tc.wantLog) {
				t.Fatalf("log missing %q:\n%s", tc.wantLog, strings.Join(logs.lines, "\n"))
			}
			evs := kindEvents(t, &buf, event.KindQARoster)
			if len(evs) != 1 {
				t.Fatalf("emitted %d qa_roster events, want exactly 1", len(evs))
			}
			ev := evs[0]
			if ev.Msg != tc.wantLog {
				t.Errorf("event msg = %q, want %q", ev.Msg, tc.wantLog)
			}
			if got := strField(ev.Fields, "ticket"); got != "COD-1" {
				t.Errorf("ticket field = %q, want %q", got, "COD-1")
			}
			if got := ev.Fields["accounts"]; got != tc.wantAccounts {
				t.Errorf("accounts field = %v, want %v", got, tc.wantAccounts)
			}
			if got := ev.Fields["notes"]; got != tc.wantNotes {
				t.Errorf("notes field = %v, want %v", got, tc.wantNotes)
			}
			if got := strField(ev.Fields, "error"); got != tc.wantErrField {
				t.Errorf("error field = %q, want %q", got, tc.wantErrField)
			}
			assertNoQASecrets(t, strings.Join(logs.lines, "\n"), buf.String())
		})
	}
}

// assertNoQASecrets is the leak guard on the observability surfaces: the injected
// note is allowed to carry credentials because it reaches only the verify prompt,
// but no label, username, or secret may appear in a log line or a durable event.
func assertNoQASecrets(t *testing.T, surfaces ...string) {
	t.Helper()
	for _, text := range surfaces {
		for _, secret := range []string{"admin", "member", "example.test", "s3cret", "hunter2", "/auth"} {
			if strings.Contains(text, secret) {
				t.Errorf("observability surface leaked %q: %s", secret, text)
			}
		}
	}
}

// TestQAVerifyNoteSilentWhenGateInactive keeps the reporting scoped to a verify
// the QA gate actually reaches: a backend slice must stay quiet rather than
// announce an injection that never applied.
func TestQAVerifyNoteSilentWhenGateInactive(t *testing.T) {
	var buf bytes.Buffer
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.Git = filesGit{files: backendFiles}
	p.Events = event.New(&buf)
	p.FetchQAAccounts = func(context.Context) (hubclient.QARoster, error) {
		return hubclient.QARoster{Accounts: []hubclient.QAAccount{{Label: "admin"}}}, nil
	}

	p.qaVerifyNote(context.Background(), "COD-1", "drive the app")

	if evs := kindEvents(t, &buf, event.KindQARoster); len(evs) != 0 {
		t.Errorf("emitted %d qa_roster events on a backend slice, want 0", len(evs))
	}
}

// TestQACredentialsConfinedToVerifyPrompt is the happy-path guarantee that a
// stored secret can only ever reach the verifier: it lands in the verify prompt
// but the handoff (QA-brief authoring) prompt has no channel for it, so the brief
// the verifier grades against can never carry a credential.
func TestQACredentialsConfinedToVerifyPrompt(t *testing.T) {
	const secret = "top-secret-pw"
	qaNote := qaRosterNote("COD-1", []hubclient.QAAccount{{Label: "admin", Username: "u", Secret: secret}}, "")

	verify := verifyTail(prompts.Renderer{}, "COD-1", handoffPath("COD-1"), verifyPath("COD-1"), "drive the app", qaNote, "", "", "", "", "")
	if !strings.Contains(verify, secret) {
		t.Fatal("verify prompt should carry the injected credentials")
	}

	brief := handoffTail(prompts.Renderer{}, "COD-1", "")
	if strings.Contains(brief, secret) {
		t.Fatal("handoff/brief prompt leaked QA credentials")
	}
}
