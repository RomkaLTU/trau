package hubstore

import (
	"errors"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testQAAccounts(t *testing.T) *QAAccounts {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewQAAccounts(db.SQL())
}

func TestQAAccountCRUD(t *testing.T) {
	q := testQAAccounts(t)
	const repo = "/repos/acme"

	created, err := q.Create(repo, QAAccountInput{
		Label:       "admin",
		Username:    "admin@example.test",
		Secret:      "s3cret",
		Description: "admin dashboard and billing flows",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 {
		t.Fatalf("create returned unstamped account: %+v", created)
	}
	if created.Secret != "s3cret" {
		t.Errorf("store returned secret %q, want the full value", created.Secret)
	}
	if created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Errorf("create left timestamps blank: %+v", created)
	}

	got, found, err := q.Get(repo, created.ID)
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got.Secret != "s3cret" || got.Username != "admin@example.test" {
		t.Errorf("get returned %+v", got)
	}

	updated, err := q.Update(repo, created.ID, QAAccountInput{
		Label:       "admin",
		Username:    "admin@example.test",
		Secret:      "rotated",
		Description: "admin dashboard, billing, and settings",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Secret != "rotated" || updated.Description != "admin dashboard, billing, and settings" {
		t.Errorf("update returned %+v", updated)
	}

	deleted, err := q.Delete(repo, created.ID)
	if err != nil || !deleted {
		t.Fatalf("delete: deleted=%v err=%v", deleted, err)
	}
	if _, found, _ := q.Get(repo, created.ID); found {
		t.Error("account still present after delete")
	}
}

func TestQAAccountUpdateMissing(t *testing.T) {
	q := testQAAccounts(t)
	if _, err := q.Update("/repos/acme", 999, QAAccountInput{Label: "ghost"}); !errors.Is(err, ErrQAAccountNotFound) {
		t.Fatalf("update missing err = %v, want ErrQAAccountNotFound", err)
	}
}

func TestQAAccountListScopedAndOrdered(t *testing.T) {
	q := testQAAccounts(t)
	if _, err := q.Create("/repos/acme", QAAccountInput{Label: "viewer"}); err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	if _, err := q.Create("/repos/acme", QAAccountInput{Label: "admin"}); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if _, err := q.Create("/repos/other", QAAccountInput{Label: "elsewhere"}); err != nil {
		t.Fatalf("create other-repo account: %v", err)
	}

	list, err := q.List("/repos/acme")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list returned %d accounts, want 2 (repo-scoped)", len(list))
	}
	if list[0].Label != "admin" || list[1].Label != "viewer" {
		t.Errorf("list not ordered by label: %q, %q", list[0].Label, list[1].Label)
	}
}

func TestQAAccountByLabel(t *testing.T) {
	q := testQAAccounts(t)
	if _, err := q.Create("/repos/acme", QAAccountInput{Label: "admin", Secret: "x"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, ok, err := q.ByLabel("/repos/acme", "admin"); err != nil || !ok {
		t.Fatalf("ByLabel(admin): ok=%v err=%v", ok, err)
	}
	if _, ok, err := q.ByLabel("/repos/acme", "missing"); err != nil || ok {
		t.Fatalf("ByLabel(missing): ok=%v err=%v", ok, err)
	}
}

func TestQANotes(t *testing.T) {
	q := testQAAccounts(t)
	if notes, err := q.Notes("/repos/acme"); err != nil || notes != "" {
		t.Fatalf("empty notes = %q err=%v, want blank", notes, err)
	}
	if err := q.SetNotes("/repos/acme", "create a disposable admin via the seeder; delete it after"); err != nil {
		t.Fatalf("set notes: %v", err)
	}
	notes, err := q.Notes("/repos/acme")
	if err != nil {
		t.Fatalf("read notes: %v", err)
	}
	if notes != "create a disposable admin via the seeder; delete it after" {
		t.Errorf("notes = %q", notes)
	}
	if err := q.SetNotes("/repos/acme", "updated"); err != nil {
		t.Fatalf("update notes: %v", err)
	}
	if notes, _ := q.Notes("/repos/acme"); notes != "updated" {
		t.Errorf("notes after upsert = %q, want updated", notes)
	}
}
