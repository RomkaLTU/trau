package webserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

// newNotifyPushServer wires a hub with one push subscription whose sender hands
// every payload to the returned channel. Delivery runs off the producer's
// goroutine, so a test reads the channel instead of a slice the producer races on.
func newNotifyPushServer(t *testing.T) (*Server, <-chan PushPayload) {
	t.Helper()
	s := New("test", "127.0.0.1", "", nil, false, testStores(t))
	delivered := make(chan PushPayload, 4)
	s.pushSend = func(
		_ context.Context,
		_ hubstore.PushSubscription,
		_ hubstore.VAPID,
		payload []byte,
	) (int, error) {
		var out PushPayload
		if err := json.Unmarshal(payload, &out); err != nil {
			t.Errorf("decode delivered payload: %v", err)
		}
		delivered <- out
		return 201, nil
	}
	if _, err := s.stores.Push().Subscribe("https://push.example/abc", "p256", "auth"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return s, delivered
}

func awaitPush(t *testing.T, delivered <-chan PushPayload) PushPayload {
	t.Helper()
	select {
	case payload := <-delivered:
		return payload
	case <-time.After(2 * time.Second):
		t.Fatal("no push delivered")
		return PushPayload{}
	}
}

func expectNoPush(t *testing.T, delivered <-chan PushPayload) {
	t.Helper()
	select {
	case payload := <-delivered:
		t.Fatalf("push delivered %+v, want none", payload)
	case <-time.After(150 * time.Millisecond):
	}
}

func stateChangeRow(t *testing.T, state, ticket, msg string) hubstore.EventRow {
	t.Helper()
	fields, err := json.Marshal(map[string]any{"state": state, "ticket": ticket})
	if err != nil {
		t.Fatalf("encode fields: %v", err)
	}
	return hubstore.EventRow{Kind: stateChangeKind, Msg: msg, Fields: string(fields)}
}

// TestNotifyRunEventPushesDeepLink pins the run producer's whole push contract: a
// needs-attention state reaches the browser with the notification's own title and
// body, the tag the in-tab path coalesces on, and a link straight to the run.
func TestNotifyRunEventPushesDeepLink(t *testing.T) {
	s, delivered := newNotifyPushServer(t)
	repo := registry.Repo{Root: filepath.Join(t.TempDir(), "demo"), Name: "demo"}

	s.notifyRunEvent(repo, stateChangeRow(t, "paused", "COD-9", "provider rate limit"))

	payload := awaitPush(t, delivered)
	want := PushPayload{
		Title: "Run paused — demo",
		Body:  "provider rate limit",
		Tag:   "run_pausedCOD-9",
		URL:   "/runs/demo/COD-9",
	}
	if payload != want {
		t.Errorf("payload = %+v, want %+v", payload, want)
	}
}

// TestNotifyRunEventPushesBudgetHalt keeps a spend ceiling on the notification path
// rather than letting it fall through unclassified: it parks the run like any other
// pause, so it rides the pause kind's tag and routing while the title names the cap.
func TestNotifyRunEventPushesBudgetHalt(t *testing.T) {
	s, delivered := newNotifyPushServer(t)
	repo := registry.Repo{Root: filepath.Join(t.TempDir(), "demo"), Name: "demo"}

	s.notifyRunEvent(repo, stateChangeRow(t, "budget", "COD-9", "per-ticket budget cap $0.50 reached"))

	payload := awaitPush(t, delivered)
	want := PushPayload{
		Title: "Run over budget — demo",
		Body:  "per-ticket budget cap $0.50 reached",
		Tag:   "run_pausedCOD-9",
		URL:   "/runs/demo/COD-9",
	}
	if payload != want {
		t.Errorf("payload = %+v, want %+v", payload, want)
	}
}

// TestNotifyRunEventPushesOnlyNeedsAttention keeps the push set to the states that
// actually want the user: a completed run stays in-tab, and so does any event kind
// the run producer does not classify.
func TestNotifyRunEventPushesOnlyNeedsAttention(t *testing.T) {
	cases := []struct {
		name string
		row  hubstore.EventRow
		push bool
	}{
		{name: "paused", row: stateChangeRow(t, "paused", "COD-1", "rate limited"), push: true},
		{name: "faulted", row: stateChangeRow(t, "faulted", "COD-2", "push failed"), push: true},
		{name: "quarantined", row: stateChangeRow(t, "quarantined", "COD-3", "gave up"), push: true},
		{name: "awaiting merge", row: stateChangeRow(t, "awaiting_merge", "COD-4", "pr open"), push: true},
		{name: "epic delivered", row: stateChangeRow(t, "epic_delivered", "COD-7", "epic COD-7 merged to main"), push: true},
		{name: "done", row: stateChangeRow(t, "done", "COD-5", "merged")},
		{name: "running", row: stateChangeRow(t, "running", "COD-6", "building")},
		{name: "other kind", row: hubstore.EventRow{Kind: "phase", Msg: "verify"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, delivered := newNotifyPushServer(t)
			repo := registry.Repo{Root: filepath.Join(t.TempDir(), "demo"), Name: "demo"}

			s.notifyRunEvent(repo, tc.row)

			if !tc.push {
				expectNoPush(t, delivered)
				return
			}
			if payload := awaitPush(t, delivered); payload.URL == "" {
				t.Errorf("payload = %+v, want a click target", payload)
			}
		})
	}
}

// TestNotifyGrillAwaitingPushesSessionLink covers the grilling producer: a session
// entering the awaiting set lands the user on the waiting question, on the surface
// its mode is answered from — in the session's own project, which both pages read
// from the link — and one that never entered it pushes nothing.
func TestNotifyGrillAwaitingPushesSessionLink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "demo")
	cases := []struct {
		name string
		sess hubstore.GrillSession
		want PushPayload
	}{
		{
			name: "tracked issue",
			sess: hubstore.GrillSession{
				ID:         42,
				Repo:       root,
				IssueID:    "COD-7",
				IssueTitle: "Ship push",
				State:      hubstore.GrillWaiting,
			},
			want: PushPayload{
				Title: "Grilling needs you — Ship push",
				Body:  "Which repo owns this?",
				Tag:   "grill_question42",
				URL:   "/inbox?issue=COD-7&repo=demo",
			},
		},
		{
			name: "authoring draft",
			sess: hubstore.GrillSession{ID: 43, Repo: root, State: hubstore.GrillParked},
			want: PushPayload{
				Title: "Grilling needs you — new issue",
				Body:  "Which repo owns this?",
				Tag:   "grill_question43",
				URL:   "/inbox?issue=draft%3A43&repo=demo",
			},
		},
		{
			name: "research session",
			sess: hubstore.GrillSession{
				ID:    45,
				Repo:  root,
				Mode:  hubstore.GrillModeResearch,
				State: hubstore.GrillWaiting,
			},
			want: PushPayload{
				Title: "Grilling needs you — new issue",
				Body:  "Which repo owns this?",
				Tag:   "grill_question45",
				URL:   "/research?repo=demo&session=45",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, delivered := newNotifyPushServer(t)

			s.notifyGrillAwaiting(tc.sess, "Which repo owns this?")

			if payload := awaitPush(t, delivered); payload != tc.want {
				t.Errorf("payload = %+v, want %+v", payload, tc.want)
			}
		})
	}

	t.Run("still running", func(t *testing.T) {
		s, delivered := newNotifyPushServer(t)

		s.notifyGrillAwaiting(hubstore.GrillSession{ID: 44, State: hubstore.GrillRunning}, "thinking")

		expectNoPush(t, delivered)
	})
}

// TestPublishNotificationWithoutSubscriptionsSendsNothing pins the settings toggle:
// disabling push drops the subscription, and the producers then have nobody to
// wake even though they keep recording and broadcasting.
func TestPublishNotificationWithoutSubscriptionsSendsNothing(t *testing.T) {
	s, delivered := newNotifyPushServer(t)
	if err := s.stores.Push().Unsubscribe("https://push.example/abc"); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	repo := registry.Repo{Root: filepath.Join(t.TempDir(), "demo"), Name: "demo"}

	s.notifyRunEvent(repo, stateChangeRow(t, "paused", "COD-8", "rate limited"))

	expectNoPush(t, delivered)
	items, err := s.stores.Notifications().List(10)
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("stored %d notifications, want the write path untouched by push being off", len(items))
	}
}

func TestNotificationPushRejectsUnknownKind(t *testing.T) {
	notif := hubstore.Notification{Kind: "run_finished", Ref: "COD-1", Title: "Run finished — demo"}

	if payload, ok := notificationPush(notif, "demo"); ok {
		t.Errorf("payload = %+v, want no push for a kind outside the needs-attention set", payload)
	}
}
