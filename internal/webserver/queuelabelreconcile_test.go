package webserver

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// reconcileLabelServer builds a hub with one registered repo carrying ini, a
// recording Writer and a Reader whose pull lands nothing new, so a sync exercises
// the queued-label reconcile against whatever the store and queue already hold.
func reconcileLabelServer(t *testing.T, ini string) (*Server, *fakeWriter, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "acme")
	writeRepoINI(t, root, ini)
	s := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStores(t))
	s.home = t.TempDir()
	writer := newFakeWriter()
	s.newWriter = func(config.Config) (tracker.Writer, error) { return writer, nil }
	s.newReader = func(config.Config) (tracker.Reader, error) { return &fakeReader{}, nil }
	return s, writer, root
}

func syncOnce(t *testing.T, s *Server, root string) {
	t.Helper()
	if _, err := s.syncRepo(context.Background(), workspaceRepo(root)); err != nil {
		t.Fatalf("sync: %v", err)
	}
}

func seedLabelledIssue(t *testing.T, s *Server, root, id, group string, labels ...string) {
	t.Helper()
	seedSyncedIssue(t, s.stores.Issues(), root, hubstore.Issue{
		Identifier:  id,
		Title:       id,
		StatusGroup: group,
		Labels:      labels,
	})
}

func TestExpectedQueuedLabel(t *testing.T) {
	states := []hubstore.LabelState{
		{Identifier: "COD-1", StatusGroup: "backlog"},
		{Identifier: "COD-10", StatusGroup: "unstarted"},
		{Identifier: "COD-11", StatusGroup: "done"},
		{Identifier: "COD-12", StatusGroup: "started"},
		{Identifier: "COD-13", StatusGroup: "backlog"},
	}
	cases := []struct {
		name  string
		items []queue.Item
		want  []string
	}{
		{
			name:  "pending ticket",
			items: []queue.Item{{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusPending}},
			want:  []string{"COD-1"},
		},
		{
			name:  "running ticket",
			items: []queue.Item{{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusRunning}},
			want:  nil,
		},
		{
			name:  "settled ticket",
			items: []queue.Item{{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusDone}},
			want:  nil,
		},
		{
			name:  "paused ticket keeps its label",
			items: []queue.Item{{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusPaused}},
			want:  []string{"COD-1"},
		},
		{
			name: "pending epic covers every captured sub-issue",
			items: []queue.Item{{
				Kind:      queue.KindEpic,
				ID:        "COD-10",
				Status:    queue.StatusPending,
				SubIssues: []queue.SubIssue{{ID: "COD-11"}, {ID: "COD-12"}, {ID: "COD-13"}},
			}},
			want: []string{"COD-10", "COD-11", "COD-12", "COD-13"},
		},
		{
			name: "running epic keeps only the planned slices",
			items: []queue.Item{{
				Kind:      queue.KindEpic,
				ID:        "COD-10",
				Status:    queue.StatusRunning,
				SubIssues: []queue.SubIssue{{ID: "COD-11"}, {ID: "COD-12"}, {ID: "COD-13"}},
			}},
			want: []string{"COD-13"},
		},
		{
			name: "issues the store has not pulled are left out",
			items: []queue.Item{{
				Kind:      queue.KindEpic,
				ID:        "COD-90",
				Status:    queue.StatusPending,
				SubIssues: []queue.SubIssue{{ID: "COD-91"}, {ID: "COD-13"}},
			}},
			want: []string{"COD-13"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expectedQueuedLabel(tc.items, states)
			want := map[string]bool{}
			for _, id := range tc.want {
				want[id] = true
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("expected set = %v, want %v", got, want)
			}
		})
	}
}

// An enqueue whose tracker write failed, and an issue queued before the label
// existed, look the same to the store: queued with no label. Both are healed.
func TestSyncAddsMissingQueuedLabel(t *testing.T) {
	s, writer, root := reconcileLabelServer(t, "QUEUED_LABEL=queued\n")
	seedLabelledIssue(t, s, root, "COD-11", "backlog")
	seedQueue(t, s, root, false, queue.Item{ID: "COD-11"})

	syncOnce(t, s, root)

	want := []labelCall{{id: "COD-11", add: []string{"queued"}}}
	if !reflect.DeepEqual(writer.labels, want) {
		t.Errorf("label writes = %+v, want %+v", writer.labels, want)
	}
	if got := storedLabels(t, s, root, "COD-11"); !reflect.DeepEqual(got, []string{"queued"}) {
		t.Errorf("stored labels = %v, want [queued]", got)
	}
}

func TestSyncRemovesStrayQueuedLabel(t *testing.T) {
	s, writer, root := reconcileLabelServer(t, "QUEUED_LABEL=queued\n")
	seedLabelledIssue(t, s, root, "COD-11", "backlog", "queued")

	syncOnce(t, s, root)

	want := []labelCall{{id: "COD-11", remove: []string{"queued"}}}
	if !reflect.DeepEqual(writer.labels, want) {
		t.Errorf("label writes = %+v, want the stray label stripped", writer.labels)
	}
	if got := storedLabels(t, s, root, "COD-11"); len(got) != 0 {
		t.Errorf("stored labels = %v, want none", got)
	}
}

func TestSyncQueuedLabelInStepWritesNothing(t *testing.T) {
	s, writer, root := reconcileLabelServer(t, "QUEUED_LABEL=queued\n")
	seedLabelledIssue(t, s, root, "COD-11", "backlog", "queued")
	seedLabelledIssue(t, s, root, "COD-12", "backlog", "other")
	seedQueue(t, s, root, false, queue.Item{ID: "COD-11"})

	syncOnce(t, s, root)

	if len(writer.labels) != 0 {
		t.Errorf("label writes = %+v, want none — the board already matches the queue", writer.labels)
	}
	if got := storedLabels(t, s, root, "COD-12"); !reflect.DeepEqual(got, []string{"other"}) {
		t.Errorf("stored labels = %v, want the unrelated label untouched", got)
	}
}

// Mid-epic the finished and in-flight slices lose the label while the ones still
// waiting keep it, and the running epic itself carries none.
func TestSyncMidEpicKeepsPlannedSlicesLabelled(t *testing.T) {
	s, writer, root := reconcileLabelServer(t, "QUEUED_LABEL=queued\n")
	seedLabelledIssue(t, s, root, "COD-10", "started", "queued")
	seedLabelledIssue(t, s, root, "COD-11", "done", "queued")
	seedLabelledIssue(t, s, root, "COD-12", "started", "queued")
	seedLabelledIssue(t, s, root, "COD-13", "unstarted")
	seedQueue(t, s, root, true, queue.Item{
		Kind:      queue.KindEpic,
		ID:        "COD-10",
		Status:    queue.StatusRunning,
		PID:       4242,
		SubIssues: []queue.SubIssue{{ID: "COD-11"}, {ID: "COD-12"}, {ID: "COD-13"}},
	})

	syncOnce(t, s, root)

	want := []labelCall{
		{id: "COD-13", add: []string{"queued"}},
		{id: "COD-10", remove: []string{"queued"}},
		{id: "COD-11", remove: []string{"queued"}},
		{id: "COD-12", remove: []string{"queued"}},
	}
	if !reflect.DeepEqual(writer.labels, want) {
		t.Errorf("label writes = %+v, want %+v", writer.labels, want)
	}
}

func TestSyncQueuedLabelEmptyReconcilesNothing(t *testing.T) {
	s, writer, root := reconcileLabelServer(t, "QUEUED_LABEL=\n")
	seedLabelledIssue(t, s, root, "COD-11", "backlog", "queued")
	seedLabelledIssue(t, s, root, "COD-12", "backlog")
	seedQueue(t, s, root, false, queue.Item{ID: "COD-12"})

	syncOnce(t, s, root)

	if len(writer.labels) != 0 {
		t.Errorf("label writes = %+v, want none with QUEUED_LABEL unset", writer.labels)
	}
	if got := storedLabels(t, s, root, "COD-11"); !reflect.DeepEqual(got, []string{"queued"}) {
		t.Errorf("stored labels = %v, want the row left alone", got)
	}
}

// Without a direct writer — GitHub, or missing credentials — the pass is skipped
// rather than mirrored onto rows the next pull would only overwrite.
func TestSyncQueuedLabelWithoutWriterSkipsReconcile(t *testing.T) {
	s, _, root := reconcileLabelServer(t, "QUEUED_LABEL=queued\n")
	s.newWriter = func(config.Config) (tracker.Writer, error) { return nil, tracker.ErrWriterUnavailable }
	seedLabelledIssue(t, s, root, "COD-11", "backlog")
	seedQueue(t, s, root, false, queue.Item{ID: "COD-11"})

	syncOnce(t, s, root)

	if got := storedLabels(t, s, root, "COD-11"); len(got) != 0 {
		t.Errorf("stored labels = %v, want none — the repo has no writer to heal with", got)
	}
}

func TestSyncQueuedLabelHealsInternalIssue(t *testing.T) {
	s, writer, root := reconcileLabelServer(t, "QUEUED_LABEL=queued\n")
	iss, err := s.stores.Issues().CreateInternal(root, "ACME", hubstore.InternalDraft{Title: "Internal work"})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}
	seedQueue(t, s, root, false, queue.Item{ID: iss.Identifier})

	syncOnce(t, s, root)

	if len(writer.labels) != 0 {
		t.Errorf("label writes = %+v, want none — an internal issue never reaches the tracker", writer.labels)
	}
	if got := storedLabels(t, s, root, iss.Identifier); !reflect.DeepEqual(got, []string{"queued"}) {
		t.Errorf("stored labels = %v, want [queued]", got)
	}
}
