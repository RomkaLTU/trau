package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/notify"
	"github.com/RomkaLTU/trau/internal/state"
)

// recordNotifier returns a Notifier that appends each "title|body" call to calls.
func recordNotifier(calls *[]string) notify.Notifier {
	return func(title, body string) error {
		*calls = append(*calls, title+"|"+body)
		return nil
	}
}

func TestAmbientTitle(t *testing.T) {
	running := initialModel(nil)
	running.currentTicket = "COD-217"
	running.steps = startPhase(phaseSteps(), "verify", time.Now())

	pausedRun := initialModel(nil)
	pausedRun.paused = true

	summaryMerged := initialModel(nil)
	summaryMerged.state = stateSummary
	summaryMerged.results = []console.TicketResult{
		{ID: "COD-1", Phase: state.Merged},
		{ID: "COD-2", Phase: state.Merged},
		{ID: "COD-3", Phase: state.Quarantined},
	}

	summaryPaused := initialModel(nil)
	summaryPaused.state = stateSummary
	summaryPaused.summary = console.SessionSummary{Paused: true}

	summaryFault := initialModel(nil)
	summaryFault.state = stateSummary
	summaryFault.summary = console.SessionSummary{Fault: true, FaultID: "COD-9"}

	cases := []struct {
		name string
		m    model
		want string
	}{
		{"idle", initialModel(nil), "trau"},
		{"running phase", running, "trau ✻ COD-217 verify"},
		{"paused mid-run", pausedRun, "trau ⚠ paused"},
		{"summary merged", summaryMerged, "trau ✓ 2 merged"},
		{"summary paused", summaryPaused, "trau ⚠ paused"},
		{"summary fault", summaryFault, "trau ⚠ needs attention"},
	}
	for _, c := range cases {
		if got := c.m.ambientTitle(); got != c.want {
			t.Errorf("%s: ambientTitle() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestViewSetsTitleAndFocus(t *testing.T) {
	m := freshDash(120, 40, "main")
	m.currentTicket = "COD-5"
	m.steps = startPhase(phaseSteps(), "build", time.Now())
	v := m.View()
	if v.WindowTitle != "trau ✻ COD-5 build" {
		t.Errorf("WindowTitle = %q, want %q", v.WindowTitle, "trau ✻ COD-5 build")
	}
	if !v.ReportFocus {
		t.Error("ReportFocus should be enabled so focus/blur reach Update")
	}
}

func TestTicketNotifyQuarantineOnly(t *testing.T) {
	var calls []string
	m := initialModel(nil)
	m.notifier = recordNotifier(&calls)

	// A merged ticket does not notify — it's tallied at session end.
	if cmd := m.ticketNotifyCmd(console.TicketResult{ID: "COD-1", Phase: state.Merged}); cmd != nil {
		t.Error("merged ticket should not produce a notification")
	}

	// A quarantine produces exactly one, carrying the id and reason.
	cmd := m.ticketNotifyCmd(console.TicketResult{ID: "COD-2", Phase: state.Quarantined, FailureReason: "build failed"})
	if cmd == nil {
		t.Fatal("quarantine should produce a notification")
	}
	cmd()
	if len(calls) != 1 {
		t.Fatalf("got %d notifications, want 1: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "COD-2 quarantined") || !strings.Contains(calls[0], "build failed") {
		t.Errorf("notification = %q, want id + reason", calls[0])
	}
}

func TestSessionNotify(t *testing.T) {
	cases := []struct {
		name    string
		summary console.SessionSummary
		results []console.TicketResult
		want    string
	}{
		{
			name:    "clean finish tallies merged and quarantined",
			summary: console.SessionSummary{},
			results: []console.TicketResult{
				{ID: "COD-1", Phase: state.Merged},
				{ID: "COD-2", Phase: state.Merged},
				{ID: "COD-3", Phase: state.Quarantined},
			},
			want: "session ended — 2 merged, 1 quarantined",
		},
		{
			name:    "paused",
			summary: console.SessionSummary{Paused: true},
			want:    "paused",
		},
		{
			name:    "fault names the ticket",
			summary: console.SessionSummary{Fault: true, FaultID: "COD-9"},
			want:    "faulted on COD-9",
		},
	}
	for _, c := range cases {
		var calls []string
		m := initialModel(nil)
		m.notifier = recordNotifier(&calls)
		m.results = c.results
		cmd := m.sessionNotifyCmd(c.summary)
		if cmd == nil {
			t.Fatalf("%s: expected a notification", c.name)
		}
		cmd()
		if len(calls) != 1 {
			t.Fatalf("%s: got %d notifications, want 1", c.name, len(calls))
		}
		if !strings.Contains(calls[0], c.want) {
			t.Errorf("%s: notification = %q, want substring %q", c.name, calls[0], c.want)
		}
	}
}

func TestSessionNotifySuppressedOnManualStop(t *testing.T) {
	var calls []string

	// A natural, unattended finish notifies.
	natural := initialModel(nil)
	natural.notifier = recordNotifier(&calls)
	if _, cmd := natural.enterSummary(console.SessionSummary{}); cmd == nil {
		t.Error("natural session end should fire a notification")
	}

	// A user-initiated stop (q / ctrl+c) does not — they're at the keyboard.
	stopped := initialModel(nil)
	stopped.notifier = recordNotifier(&calls)
	stopped.stopping = true
	if _, cmd := stopped.enterSummary(console.SessionSummary{}); cmd != nil {
		t.Error("manual stop should not fire a session notification")
	}
}

func TestNotifyDisabledIsNoOp(t *testing.T) {
	m := initialModel(nil) // nil notifier
	if cmd := m.ticketNotifyCmd(console.TicketResult{ID: "COD-1", Phase: state.Quarantined}); cmd != nil {
		t.Error("nil notifier should yield a nil ticket cmd")
	}
	if cmd := m.sessionNotifyCmd(console.SessionSummary{}); cmd != nil {
		t.Error("nil notifier should yield a nil session cmd")
	}
}

func TestBuildRecap(t *testing.T) {
	cases := []struct {
		name          string
		before, after map[string]string
		want          string
	}{
		{
			name:   "merge and phase advance",
			before: map[string]string{"COD-216": "@build", "COD-217": "@build"},
			after:  map[string]string{"COD-216": state.Merged, "COD-217": "@verify"},
			want:   "while you were away: COD-216 merged · COD-217 reached verify",
		},
		{
			name:   "ticket started and quarantined while away",
			before: map[string]string{},
			after:  map[string]string{"COD-9": state.Quarantined},
			want:   "while you were away: COD-9 quarantined",
		},
		{
			name:   "no changes",
			before: map[string]string{"COD-1": "@verify"},
			after:  map[string]string{"COD-1": "@verify"},
			want:   "while you were away: no state changes",
		},
	}
	for _, c := range cases {
		if got := buildRecap(c.before, c.after); got != c.want {
			t.Errorf("%s: buildRecap() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRecapFocusThreshold(t *testing.T) {
	base := func() model {
		m := initialModel(nil)
		m.currentTicket = "COD-1"
		m.steps = startPhase(phaseSteps(), "verify", time.Now())
		m.blurSnapshot = map[string]string{"COD-1": "@build"}
		return m
	}

	// Away long enough → recap banner reflecting the phase advance.
	long := base()
	long.blurAt = time.Now().Add(-4 * time.Minute)
	long.onFocus()
	if long.recapBanner != "while you were away: COD-1 reached verify" {
		t.Errorf("long-away banner = %q", long.recapBanner)
	}

	// Brief blur → no banner.
	short := base()
	short.blurAt = time.Now().Add(-30 * time.Second)
	short.onFocus()
	if short.recapBanner != "" {
		t.Errorf("brief blur should not show a banner, got %q", short.recapBanner)
	}

	// The next key retires it.
	long.recapBanner = long.dismissRecap().recapBanner
	if long.recapBanner != "" {
		t.Error("dismissRecap should clear the banner")
	}
}

func TestBlurSnapshotsState(t *testing.T) {
	m := initialModel(nil)
	m.currentTicket = "COD-1"
	m.steps = startPhase(phaseSteps(), "handoff", time.Now())
	m.results = []console.TicketResult{{ID: "COD-0", Phase: state.Merged}}
	m.onBlur()
	if m.blurAt.IsZero() {
		t.Error("onBlur should record the blur time")
	}
	if m.blurSnapshot["COD-0"] != state.Merged {
		t.Errorf("snapshot missing terminal result: %v", m.blurSnapshot)
	}
	if m.blurSnapshot["COD-1"] != "@handoff" {
		t.Errorf("snapshot missing live phase: %v", m.blurSnapshot)
	}
}
