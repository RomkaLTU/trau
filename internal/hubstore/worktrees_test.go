package hubstore

import (
	"errors"
	"testing"
)

func testWorktrees(t *testing.T) *Worktrees {
	t.Helper()
	return NewWorktrees(testDB(t))
}

// TestWorktreeRecordIsKeyedByTicket pins the idempotence provisioning depends on:
// a resume that adopts the same tree refreshes the row it already has, keeping the
// id and the created stamp, rather than filing a second row for one ticket.
func TestWorktreeRecordIsKeyedByTicket(t *testing.T) {
	w := testWorktrees(t)

	first, err := w.Record("/repo/acme", WorktreeInput{Ticket: "COD-1", Path: "/w/acme/COD-1"})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if first.State != WorktreeActive {
		t.Errorf("state = %q, want %q by default", first.State, WorktreeActive)
	}

	second, err := w.Record("/repo/acme", WorktreeInput{
		Ticket: "COD-1", Path: "/w/acme/COD-1", Branch: "feature/COD-1-x",
	})
	if err != nil {
		t.Fatalf("re-record: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("id = %d, want the same row %d", second.ID, first.ID)
	}
	if second.CreatedAt != first.CreatedAt {
		t.Errorf("created_at = %q, want the original %q", second.CreatedAt, first.CreatedAt)
	}
	if second.Branch != "feature/COD-1-x" {
		t.Errorf("branch = %q, want the adopted branch", second.Branch)
	}

	rows, err := w.List("/repo/acme")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("list = %d rows, want 1", len(rows))
	}
}

func TestWorktreeRequiresATicketAndAPath(t *testing.T) {
	w := testWorktrees(t)

	for _, in := range []WorktreeInput{
		{Ticket: "", Path: "/w/acme/COD-1"},
		{Ticket: "COD-1", Path: "  "},
	} {
		if _, err := w.Record("/repo/acme", in); !errors.Is(err, ErrWorktreeRequired) {
			t.Errorf("Record(%+v) err = %v, want ErrWorktreeRequired", in, err)
		}
	}
	if _, err := w.Record("/repo/acme", WorktreeInput{Ticket: "COD-1", Path: "/w", State: "nonsense"}); err == nil {
		t.Error("an unknown state was accepted")
	}
}

// TestWorktreeSettleAndOrphanAreScopedToTheirRepo covers the two lifecycle moves
// the hub makes — a settle when a ticket ships, an orphan when a directory vanished
// — and that neither reaches another repo's row for the same ticket id.
func TestWorktreeSettleAndOrphanAreScopedToTheirRepo(t *testing.T) {
	w := testWorktrees(t)
	if _, err := w.Record("/repo/acme", WorktreeInput{Ticket: "COD-1", Path: "/w/acme/COD-1"}); err != nil {
		t.Fatal(err)
	}
	other, err := w.Record("/repo/other", WorktreeInput{Ticket: "COD-1", Path: "/w/other/COD-1"})
	if err != nil {
		t.Fatal(err)
	}

	settled, err := w.SettleTicket("/repo/acme", "COD-1")
	if err != nil || !settled {
		t.Fatalf("SettleTicket = (%v, %v), want (true, nil)", settled, err)
	}
	row, ok, err := w.ByTicket("/repo/acme", "COD-1")
	if err != nil || !ok || row.State != WorktreeSettled {
		t.Fatalf("acme row = (%+v, %v, %v), want settled", row, ok, err)
	}
	if row, ok, err := w.ByTicket("/repo/other", "COD-1"); err != nil || !ok || row.State != WorktreeActive {
		t.Fatalf("other repo's row = (%+v, %v, %v), want still active", row, ok, err)
	}

	if _, err := w.SetState("/repo/other", other.ID, WorktreeOrphaned); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	active, err := w.Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("Active = %+v, want none left", active)
	}

	if _, err := w.SetState("/repo/acme", 9999, WorktreeSettled); !errors.Is(err, ErrWorktreeNotFound) {
		t.Errorf("SetState on an unknown id = %v, want ErrWorktreeNotFound", err)
	}
	if settled, err := w.SettleTicket("/repo/acme", "COD-404"); err != nil || settled {
		t.Errorf("SettleTicket on an unknown ticket = (%v, %v), want (false, nil)", settled, err)
	}
}

func TestWorktreeGetAndDelete(t *testing.T) {
	w := testWorktrees(t)
	row, err := w.Record("/repo/acme", WorktreeInput{Ticket: "COD-1", Path: "/w/acme/COD-1"})
	if err != nil {
		t.Fatal(err)
	}

	if got, ok, err := w.Get("/repo/acme", row.ID); err != nil || !ok || got.Path != "/w/acme/COD-1" {
		t.Fatalf("Get = (%+v, %v, %v)", got, ok, err)
	}
	if _, ok, err := w.Get("/repo/acme", 9999); err != nil || ok {
		t.Errorf("Get on an unknown id = (%v, %v), want (false, nil)", ok, err)
	}
	if deleted, err := w.Delete("/repo/acme", row.ID); err != nil || !deleted {
		t.Fatalf("Delete = (%v, %v), want (true, nil)", deleted, err)
	}
	if deleted, err := w.Delete("/repo/acme", row.ID); err != nil || deleted {
		t.Errorf("second Delete = (%v, %v), want (false, nil)", deleted, err)
	}
}
