package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubclient"
)

// captureHub records what the ingest asked the hub to store and can fail on
// demand, standing in for the hub's create endpoint.
type captureHub struct {
	saved []hubclient.QAAccountInput
	err   error
}

func (h *captureHub) save(_ context.Context, in hubclient.QAAccountInput) error {
	if h.err != nil {
		return h.err
	}
	h.saved = append(h.saved, in)
	return nil
}

func writeQACapture(t *testing.T, id string, accounts ...qaCaptureAccount) {
	t.Helper()
	data, err := json.Marshal(qaCaptureFile{Accounts: accounts})
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	writeQACaptureRaw(t, id, string(data))
}

func writeQACaptureRaw(t *testing.T, id, body string) {
	t.Helper()
	path := qaCapturePath(id)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write capture file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
}

func newCapturePipeline(t *testing.T, hub *captureHub, roster ...hubclient.QAAccount) (*Pipeline, *logRenderer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logs := &logRenderer{}
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.Events = event.New(&buf)
	p.Renderer = logs
	p.SaveQAAccount = hub.save
	p.qaRoster = roster
	return p, logs, &buf
}

func assertQACaptureConsumed(t *testing.T, id string) {
	t.Helper()
	if _, err := os.Stat(qaCapturePath(id)); !os.IsNotExist(err) {
		t.Errorf("capture file still present after ingest (stat err = %v)", err)
	}
}

// TestIngestQACaptureStoresOnlyNewAccounts is the acceptance case: of two offered
// accounts the one already in the roster the verifier was handed is skipped, the
// genuinely new one is stored as agent-captured, and the file is consumed.
func TestIngestQACaptureStoresOnlyNewAccounts(t *testing.T) {
	const id = "COD-91078"
	hub := &captureHub{}
	p, logs, buf := newCapturePipeline(t, hub, hubclient.QAAccount{Label: "admin", Username: "Admin@Example.Test"})
	writeQACapture(t, id,
		qaCaptureAccount{Label: "known", Username: " admin@example.test ", Secret: "s3cret"},
		qaCaptureAccount{Label: "seeded owner", Username: "owner@example.test", Secret: "hunter2", Description: "owner dashboard; database/seeders"},
	)

	p.ingestQACapture(context.Background(), id)

	if len(hub.saved) != 1 {
		t.Fatalf("saved %d accounts, want 1: %+v", len(hub.saved), hub.saved)
	}
	got := hub.saved[0]
	want := hubclient.QAAccountInput{
		Label:       "seeded owner",
		Username:    "owner@example.test",
		Secret:      "hunter2",
		Description: "owner dashboard; database/seeders",
		Source:      hubclient.QASourceAgent,
	}
	if got != want {
		t.Errorf("saved %+v, want %+v", got, want)
	}
	if !logs.contains("QA account captured: seeded owner") {
		t.Errorf("no capture log line:\n%s", strings.Join(logs.lines, "\n"))
	}
	assertQACaptureConsumed(t, id)

	evs := kindEvents(t, buf, event.KindQACaptured)
	if len(evs) != 1 {
		t.Fatalf("emitted %d qa_captured events, want 1", len(evs))
	}
	if got := strField(evs[0].Fields, "ticket"); got != id {
		t.Errorf("ticket field = %q, want %q", got, id)
	}
	if got := strField(evs[0].Fields, "label"); got != "seeded owner" {
		t.Errorf("label field = %q, want %q", got, "seeded owner")
	}
	assertNoCaptureLeak(t, logs, buf, "owner@example.test", "hunter2", "s3cret")
}

// TestIngestQACaptureValidatesEntries drops what cannot be signed in with and
// names the account generically when the verifier named no label, so the
// username never reaches the log line or the event through it.
func TestIngestQACaptureValidatesEntries(t *testing.T) {
	const id = "COD-91079"
	hub := &captureHub{}
	p, logs, buf := newCapturePipeline(t, hub)
	writeQACapture(t, id,
		qaCaptureAccount{Label: "no secret", Username: "a@example.test"},
		qaCaptureAccount{Label: "no username", Secret: "pw"},
		qaCaptureAccount{Username: "  unlabeled@example.test  ", Secret: "  pw  "},
	)

	p.ingestQACapture(context.Background(), id)

	if len(hub.saved) != 1 {
		t.Fatalf("saved %d accounts, want 1: %+v", len(hub.saved), hub.saved)
	}
	if got := hub.saved[0]; got.Label != qaDiscoveredLabel || got.Username != "unlabeled@example.test" || got.Secret != "pw" {
		t.Errorf("saved %+v, want the trimmed credentials under %q", got, qaDiscoveredLabel)
	}
	assertNoCaptureLeak(t, logs, buf, "unlabeled@example.test", "pw")
}

// TestIngestQACaptureNeverLabelsWithCredentials is the leak guard on the one
// field capture observability is allowed to carry: a verifier that hands back its
// own username or secret as the label must not have it echoed into the loop log
// or the qa_captured event.
func TestIngestQACaptureNeverLabelsWithCredentials(t *testing.T) {
	const id = "COD-91090"
	hub := &captureHub{}
	p, logs, buf := newCapturePipeline(t, hub)
	writeQACapture(t, id,
		qaCaptureAccount{Label: "Seeded@Example.Test", Username: "seeded@example.test", Secret: "pw"},
		qaCaptureAccount{Label: "hunter2", Username: "owner@example.test", Secret: "hunter2"},
	)

	p.ingestQACapture(context.Background(), id)

	if len(hub.saved) != 2 {
		t.Fatalf("saved %d accounts, want 2: %+v", len(hub.saved), hub.saved)
	}
	want := []string{qaDiscoveredLabel, qaDiscoveredLabel + " (captured)"}
	for i, w := range want {
		if got := hub.saved[i].Label; got != w {
			t.Errorf("saved[%d] label = %q, want %q", i, got, w)
		}
	}
	assertNoCaptureLeak(t, logs, buf, "Seeded@Example.Test", "seeded@example.test", "owner@example.test", "hunter2")
}

// assertNoCaptureLeak holds the capture observability to labels and counts: none
// of the given credential values may appear in a log line or a durable event.
func assertNoCaptureLeak(t *testing.T, logs *logRenderer, buf *bytes.Buffer, credentials ...string) {
	t.Helper()
	for _, c := range credentials {
		if logs.contains(c) || strings.Contains(buf.String(), c) {
			t.Errorf("capture observability leaked %q:\n%s\n%s", c, strings.Join(logs.lines, "\n"), buf.String())
		}
	}
}

// TestIngestQACaptureSuffixesCollidingLabel keeps a captured account from
// colliding with a stored label the repo already uses for another login.
func TestIngestQACaptureSuffixesCollidingLabel(t *testing.T) {
	const id = "COD-91080"
	hub := &captureHub{}
	p, _, _ := newCapturePipeline(t, hub, hubclient.QAAccount{Label: "admin", Username: "stored@example.test"})
	writeQACapture(t, id, qaCaptureAccount{Label: "admin", Username: "found@example.test", Secret: "pw"})

	p.ingestQACapture(context.Background(), id)

	if len(hub.saved) != 1 {
		t.Fatalf("saved %d accounts, want 1", len(hub.saved))
	}
	if got := hub.saved[0].Label; got != "admin (captured)" {
		t.Errorf("saved label = %q, want %q", got, "admin (captured)")
	}
}

// TestIngestQACaptureCapsEntries bounds what one attempt can add to the roster
// and says so rather than silently dropping the tail.
func TestIngestQACaptureCapsEntries(t *testing.T) {
	const id = "COD-91081"
	hub := &captureHub{}
	p, logs, _ := newCapturePipeline(t, hub)
	offered := make([]qaCaptureAccount, 0, qaCaptureMax+2)
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		offered = append(offered, qaCaptureAccount{Label: n, Username: n + "@example.test", Secret: "pw"})
	}
	writeQACapture(t, id, offered...)

	p.ingestQACapture(context.Background(), id)

	if len(hub.saved) != qaCaptureMax {
		t.Fatalf("saved %d accounts, want the %d cap", len(hub.saved), qaCaptureMax)
	}
	if !logs.contains("keeping the first") {
		t.Errorf("truncation went unlogged:\n%s", strings.Join(logs.lines, "\n"))
	}
}

// TestIngestQACaptureDedupsAcrossAttempts covers the repair retry and the
// browser re-verify: the second attempt is handed the same prompt, so it offers
// the credential the first attempt already stored — under whatever label it
// chose that time — and must not create a second account for it.
func TestIngestQACaptureDedupsAcrossAttempts(t *testing.T) {
	const id = "COD-91087"
	hub := &captureHub{}
	p, _, _ := newCapturePipeline(t, hub)
	writeQACapture(t, id, qaCaptureAccount{Label: "owner", Username: "owner@example.test", Secret: "pw"})
	p.ingestQACapture(context.Background(), id)

	writeQACapture(t, id, qaCaptureAccount{Label: "seeded owner", Username: " Owner@Example.Test ", Secret: "pw"})
	p.ingestQACapture(context.Background(), id)

	if len(hub.saved) != 1 {
		t.Fatalf("saved %d accounts, want 1: %+v", len(hub.saved), hub.saved)
	}
	if got := hub.saved[0].Label; got != "owner" {
		t.Errorf("saved label = %q, want the first attempt's %q", got, "owner")
	}
}

// TestIngestQACaptureCapsAcrossAttempts holds the roster-pollution bound over a
// whole verify, not one attempt of it: repairs plus a re-verify may not multiply
// the cap.
func TestIngestQACaptureCapsAcrossAttempts(t *testing.T) {
	const id = "COD-91088"
	hub := &captureHub{}
	p, logs, _ := newCapturePipeline(t, hub)
	for _, batch := range [][]string{{"a", "b", "c"}, {"d", "e", "f"}} {
		offered := make([]qaCaptureAccount, 0, len(batch))
		for _, n := range batch {
			offered = append(offered, qaCaptureAccount{Label: n, Username: n + "@example.test", Secret: "pw"})
		}
		writeQACapture(t, id, offered...)
		p.ingestQACapture(context.Background(), id)
	}

	if len(hub.saved) != qaCaptureMax {
		t.Fatalf("saved %d accounts across two attempts, want the %d cap", len(hub.saved), qaCaptureMax)
	}
	if !logs.contains("keeping the first") {
		t.Errorf("truncation went unlogged:\n%s", strings.Join(logs.lines, "\n"))
	}
}

// TestQAVerifyNoteResetsCaptureBudget scopes the cap to one verify: the next
// ticket starts with the full allowance and the previous roster gone.
func TestQAVerifyNoteResetsCaptureBudget(t *testing.T) {
	hub := &captureHub{}
	p, _, _ := newCapturePipeline(t, hub, hubclient.QAAccount{Label: "stale", Username: "stale@example.test"})
	p.qaCaptured = qaCaptureMax
	p.Git = filesGit{files: uiFiles}
	p.FetchQAAccounts = func(context.Context) (hubclient.QARoster, error) {
		return hubclient.QARoster{}, nil
	}

	p.qaVerifyNote(context.Background(), "COD-91089", "drive the app")

	if p.qaCaptured != 0 || len(p.qaRoster) != 0 {
		t.Errorf("captured=%d roster=%+v, want a cleared budget and roster", p.qaCaptured, p.qaRoster)
	}
}

// TestIngestQACaptureToleratesFailure covers every way capture can go wrong —
// malformed file, unreachable hub, no file at all. Each one warns at most and
// leaves nothing behind; none may fail the run.
func TestIngestQACaptureToleratesFailure(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		body    string
		hubErr  error
		wantLog string
	}{
		{
			name:    "malformed file",
			id:      "COD-91082",
			body:    "{not json",
			wantLog: "unreadable capture file",
		},
		{
			name:    "hub rejects the account",
			id:      "COD-91083",
			body:    `{"accounts":[{"label":"admin","username":"a@example.test","secret":"pw"}]}`,
			hubErr:  errors.New("hub down"),
			wantLog: "QA account not captured: hub down",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := &captureHub{err: tc.hubErr}
			p, logs, buf := newCapturePipeline(t, hub)
			writeQACaptureRaw(t, tc.id, tc.body)

			p.ingestQACapture(context.Background(), tc.id)

			if !logs.contains(tc.wantLog) {
				t.Errorf("log missing %q:\n%s", tc.wantLog, strings.Join(logs.lines, "\n"))
			}
			if len(hub.saved) != 0 {
				t.Errorf("saved %d accounts, want 0", len(hub.saved))
			}
			if evs := kindEvents(t, buf, event.KindQACaptured); len(evs) != 0 {
				t.Errorf("emitted %d qa_captured events, want 0", len(evs))
			}
			assertQACaptureConsumed(t, tc.id)
		})
	}

	t.Run("no capture file", func(t *testing.T) {
		hub := &captureHub{}
		p, logs, _ := newCapturePipeline(t, hub)

		p.ingestQACapture(context.Background(), "COD-91084")

		if len(hub.saved) != 0 || len(logs.lines) != 0 {
			t.Errorf("absent capture file was not silent: saved=%+v logs=%v", hub.saved, logs.lines)
		}
	})
}

// TestVerifyAttemptIngestsCapture wires the ingest to the attempt it brackets: a
// verifier that leaves a capture file behind has it read and consumed, and a
// stale file from an earlier attempt is cleared before the verifier runs so its
// credentials are never re-captured.
func TestVerifyAttemptIngestsCapture(t *testing.T) {
	const id = "COD-91085"
	hub := &captureHub{}
	p, _, _ := newCapturePipeline(t, hub)
	p.Runner = &captureWritingRunner{id: id, verdictPath: verifyPath(id)}
	t.Cleanup(func() {
		_ = os.Remove(verifyPath(id))
		_ = os.Remove(qaCapturePath(id))
	})
	writeQACaptureRaw(t, id, `{"accounts":[{"label":"stale","username":"stale@example.test","secret":"pw"}]}`)

	if _, err := p.verifyAttempt(context.Background(), id, "verify", "", "drive the app", "qa note", "", "", "", "", "", ""); err != nil {
		t.Fatalf("verifyAttempt: %v", err)
	}

	if len(hub.saved) != 1 {
		t.Fatalf("saved %d accounts, want only the one the attempt wrote: %+v", len(hub.saved), hub.saved)
	}
	if got := hub.saved[0].Label; got != "fresh" {
		t.Errorf("captured %q, want the attempt's own account", got)
	}
	assertQACaptureConsumed(t, id)
}

// TestVerifyAttemptSkipsCaptureWithoutQAGate keeps the side channel off a slice
// the QA gate never reached: a file left over from elsewhere is not ingested.
func TestVerifyAttemptSkipsCaptureWithoutQAGate(t *testing.T) {
	const id = "COD-91086"
	hub := &captureHub{}
	p, _, _ := newCapturePipeline(t, hub)
	p.Runner = &captureWritingRunner{id: id, verdictPath: verifyPath(id)}
	t.Cleanup(func() { _ = os.Remove(verifyPath(id)) })
	writeQACaptureRaw(t, id, `{"accounts":[{"label":"stale","username":"stale@example.test","secret":"pw"}]}`)

	if _, err := p.verifyAttempt(context.Background(), id, "verify", "", "drive the app", "", "", "", "", "", "", ""); err != nil {
		t.Fatalf("verifyAttempt: %v", err)
	}

	if len(hub.saved) != 0 {
		t.Errorf("saved %d accounts without an active QA gate, want 0", len(hub.saved))
	}
}

// captureWritingRunner stands in for a verifier that both writes its verdict and
// drops a discovered credential into the capture side channel.
type captureWritingRunner struct {
	id          string
	verdictPath string
}

func (r *captureWritingRunner) Run(context.Context, string, string) (agent.Result, error) {
	_ = os.WriteFile(r.verdictPath, []byte(`{"pass":true,"summary":"ok","browser":"driven"}`), 0o600)
	_ = os.WriteFile(qaCapturePath(r.id), []byte(`{"accounts":[{"label":"fresh","username":"fresh@example.test","secret":"pw"}]}`), 0o600)
	return agent.Result{}, nil
}
