package hubstore

import (
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testPush(t *testing.T) *Push {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewPush(db.SQL())
}

func TestPushVAPIDIsWrittenOnce(t *testing.T) {
	push := testPush(t)

	empty, err := push.VAPID()
	if err != nil {
		t.Fatalf("read empty identity: %v", err)
	}
	if empty.PublicKey != "" || empty.PrivateKey != "" {
		t.Fatalf("identity = %+v before any write, want the zero value", empty)
	}

	saved, err := push.SaveVAPID(VAPID{PublicKey: "pub", PrivateKey: "priv"})
	if err != nil {
		t.Fatalf("save identity: %v", err)
	}
	if saved.PublicKey != "pub" || saved.PrivateKey != "priv" {
		t.Fatalf("saved = %+v, want the written pair", saved)
	}

	later, err := push.SaveVAPID(VAPID{PublicKey: "other", PrivateKey: "other"})
	if err != nil {
		t.Fatalf("save second identity: %v", err)
	}
	if later != saved {
		t.Errorf("identity = %+v after a second write, want the first pair to win", later)
	}
}

func TestPushSubscriptionsUpsertOnEndpoint(t *testing.T) {
	push := testPush(t)

	first, err := push.Subscribe("https://push.example/a", "p256-a", "auth-a")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, err := push.Subscribe("https://push.example/b", "p256-b", "auth-b"); err != nil {
		t.Fatalf("subscribe second: %v", err)
	}
	refreshed, err := push.Subscribe("https://push.example/a", "p256-c", "auth-c")
	if err != nil {
		t.Fatalf("re-subscribe: %v", err)
	}
	if refreshed.ID != first.ID {
		t.Errorf("re-subscribe id = %d, want the existing row %d", refreshed.ID, first.ID)
	}

	subs, err := push.Subscriptions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("subscriptions = %d, want 2", len(subs))
	}
	if subs[0].P256dh != "p256-c" || subs[0].Auth != "auth-c" {
		t.Errorf("keys = %q/%q, want the refreshed pair", subs[0].P256dh, subs[0].Auth)
	}

	if err := push.Unsubscribe("https://push.example/a"); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if err := push.Unsubscribe("https://push.example/a"); err != nil {
		t.Fatalf("repeat unsubscribe: %v", err)
	}
	subs, err = push.Subscriptions()
	if err != nil {
		t.Fatalf("list after unsubscribe: %v", err)
	}
	if len(subs) != 1 || subs[0].Endpoint != "https://push.example/b" {
		t.Fatalf("subscriptions = %+v, want only the untouched endpoint", subs)
	}
}
