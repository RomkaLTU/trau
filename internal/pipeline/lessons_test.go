package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// newLesson-style helper for terser table rows.
func lsn(ticket, ftype, text string, tags ...string) lesson {
	return lesson{Ticket: ticket, FailureType: ftype, Lesson: text, Tags: tags}
}

// fakeLedger is an in-memory LessonStore holding records in append order (oldest
// first), the order the hub-backed store returns them in for the relevance scan.
type fakeLedger struct {
	lessons []hubclient.Lesson
	allErr  error
}

func (f *fakeLedger) Append(l hubclient.Lesson) error {
	f.lessons = append(f.lessons, l)
	return nil
}

func (f *fakeLedger) All() ([]hubclient.Lesson, error) {
	if f.allErr != nil {
		return nil, f.allErr
	}
	return append([]hubclient.Lesson(nil), f.lessons...), nil
}

func (f *fakeLedger) records() []lesson {
	out := make([]lesson, len(f.lessons))
	for i, w := range f.lessons {
		out[i] = lessonFromWire(w)
	}
	return out
}

func seedLedger(lessons ...lesson) *fakeLedger {
	f := &fakeLedger{}
	for _, l := range lessons {
		f.lessons = append(f.lessons, l.wire())
	}
	return f
}

func TestAppendLessonRecordsThroughLedger(t *testing.T) {
	led := &fakeLedger{}
	p := &Pipeline{Lessons: true, LessonLedger: led}

	want := []lesson{
		lsn("COD-1", "migration", "run migrations before seeding", "migration"),
		lsn("COD-2", "test", "assert exact json key casing", "test"),
	}
	for _, l := range want {
		p.appendLesson(l)
	}

	got := led.records()
	if len(got) != len(want) {
		t.Fatalf("ledger holds %d records, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Ticket != want[i].Ticket || got[i].Lesson != want[i].Lesson {
			t.Errorf("record %d = %+v, want ticket=%s lesson=%q", i, got[i], want[i].Ticket, want[i].Lesson)
		}
	}
}

func TestAppendLessonDisabledIsNoOp(t *testing.T) {
	led := &fakeLedger{}
	p := &Pipeline{Lessons: false, LessonLedger: led}
	p.appendLesson(lsn("COD-1", "test", "nope"))
	if len(led.records()) != 0 {
		t.Fatalf("disabled pipeline recorded %d records, want 0", len(led.records()))
	}

	// A nil ledger disables recording without a panic.
	(&Pipeline{Lessons: true}).appendLesson(lsn("COD-1", "test", "nope"))
}

// TestRecallLessonsParity checks that recalling through the hub-backed ledger
// selects exactly the lessons the file era did on identical inputs — the recall
// runs the same relevance scan over the same records, so its output must match
// relevantLessons run directly over that ledger.
func TestRecallLessonsParity(t *testing.T) {
	records := []lesson{
		lsn("COD-1", "migration", "run migrations before seeding to avoid foreign key errors", "migration"),
		lsn("COD-2", "test", "assertions on json shape need exact key casing", "test"),
		lsn("COD-3", "migration", "newer migration takeaway", "migration"),
	}
	p := &Pipeline{Lessons: true, LessonLedger: seedLedger(records...)}

	for _, query := range []string{"migration foreign key constraint", "json assert casing", "ui browser selector"} {
		want := relevantLessons(records, query, maxInjectedLessons)
		got := p.recallLessons(query)
		if len(got) != len(want) {
			t.Fatalf("recall(%q) = %v, want %v", query, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("recall(%q)[%d] = %q, want %q", query, i, got[i], want[i])
			}
		}
	}

	// Disabled or ledger-less recall injects nothing.
	if got := (&Pipeline{Lessons: false, LessonLedger: seedLedger(records...)}).recallLessons("migration"); got != nil {
		t.Errorf("disabled recall = %v, want nil", got)
	}
	if got := (&Pipeline{Lessons: true, LessonLedger: &fakeLedger{allErr: errors.New("ledger unreachable")}}).recallLessons("migration"); got != nil {
		t.Errorf("recall on a ledger error = %v, want nil", got)
	}
}

// siblingTracker answers the epic hierarchy the lessons recall walks: a ticket's
// parent epic and that epic's children. It counts the lookups so a test can prove
// build and verify share one resolution.
type siblingTracker struct {
	fakeTracker
	parent      string
	children    []string
	subErr      error
	subCalls    int
	parentCalls int
}

func (t *siblingTracker) ParentIssue(context.Context, string) (string, error) {
	t.parentCalls++
	return t.parent, nil
}

func (t *siblingTracker) SubIssues(context.Context, string) ([]tracker.SubIssue, error) {
	t.subCalls++
	if t.subErr != nil {
		return nil, t.subErr
	}
	out := make([]tracker.SubIssue, 0, len(t.children))
	for _, c := range t.children {
		out = append(out, tracker.SubIssue{ID: c})
	}
	return out, nil
}

// siblingPipeline builds a lessons-enabled pipeline for ticket id with the given
// title on its checkpoint and records in its ledger.
func siblingPipeline(t *testing.T, id, title string, trk tracker.Tracker, records ...lesson) *Pipeline {
	t.Helper()
	st := state.NewStore(t.TempDir())
	if err := st.Set(id, "TITLE", title); err != nil {
		t.Fatalf("seed title for %s: %v", id, err)
	}
	return &Pipeline{Lessons: true, LessonLedger: seedLedger(records...), State: st, Tracker: trk}
}

// TestRecallTicketLessonsInjectsEpicSiblingsWithoutKeywordOverlap covers the case
// the feature exists for: a ticket shipping the very defect its epic sibling was
// repaired for, because the disjoint titles scored the sibling's lesson below the
// relevance floor.
func TestRecallTicketLessonsInjectsEpicSiblingsWithoutKeywordOverlap(t *testing.T) {
	sibling := lsn("COD-1253", "ui", "switching repos silently reverts a selected Research session type back to Interview", "ui")
	records := []lesson{sibling}
	title := "Pre-select from the focus note"

	// The keyword path alone finds nothing — the titles are disjoint.
	if got := relevantLessons(records, title+" COD-1254", maxInjectedLessons); got != nil {
		t.Fatalf("keyword recall unexpectedly matched %v — the fixture no longer proves the gap", got)
	}

	trk := &siblingTracker{parent: "COD-1252", children: []string{"COD-1253", "COD-1254"}}
	p := siblingPipeline(t, "COD-1254", title, trk, records...)

	got := p.recallTicketLessons(context.Background(), "COD-1254")
	if len(got) != 1 || got[0] != sibling.Lesson {
		t.Fatalf("recall = %v, want the sibling lesson %q", got, sibling.Lesson)
	}

	if trk.subCalls != 1 || trk.parentCalls != 1 {
		t.Errorf("hierarchy lookups = %d sub / %d parent, want 1 each", trk.subCalls, trk.parentCalls)
	}
	if got2 := p.recallTicketLessons(context.Background(), "COD-1254"); len(got2) != 1 {
		t.Fatalf("second recall = %v, want the same single lesson", got2)
	}
	if trk.subCalls != 1 {
		t.Errorf("SubIssues called %d times across two recalls, want 1 (memoized)", trk.subCalls)
	}

	epicRun := siblingPipeline(t, "COD-1254", title, &siblingTracker{children: []string{"COD-1253", "COD-1254"}}, records...)
	epicRun.EpicID = "COD-1252"
	if got := epicRun.recallTicketLessons(context.Background(), "COD-1254"); len(got) != 1 || got[0] != sibling.Lesson {
		t.Errorf("epic-flow recall = %v, want the sibling lesson", got)
	}
}

// TestRecallTicketLessonsCapsAndOrders pins the split budget: at most
// maxSiblingLessons sibling takeaways, newest first, ahead of the keyword matches,
// with the overall maxInjectedLessons cap intact.
func TestRecallTicketLessonsCapsAndOrders(t *testing.T) {
	records := []lesson{
		lsn("COD-900", "migration", "keyword A about migration", "migration"),
		lsn("COD-901", "migration", "keyword B about migration", "migration"),
		lsn("COD-902", "migration", "keyword C about migration", "migration"),
		lsn("COD-10", "ui", "sibling oldest", "ui"),
		lsn("COD-11", "ui", "sibling middle", "ui"),
		lsn("COD-12", "ui", "sibling newer", "ui"),
		lsn("COD-13", "ui", "sibling newest", "ui"),
	}
	trk := &siblingTracker{parent: "COD-1", children: []string{"COD-10", "COD-11", "COD-12", "COD-13", "COD-14"}}
	p := siblingPipeline(t, "COD-14", "migration rollout", trk, records...)

	got := p.recallTicketLessons(context.Background(), "COD-14")
	if len(got) != maxInjectedLessons {
		t.Fatalf("recall returned %d lessons, want the cap of %d: %v", len(got), maxInjectedLessons, got)
	}
	wantHead := []string{"sibling newest", "sibling newer", "sibling middle"}
	for i, want := range wantHead {
		if got[i] != want {
			t.Errorf("recall[%d] = %q, want %q (siblings newest-first, ahead of keyword matches)", i, got[i], want)
		}
	}
	if len(wantHead) != maxSiblingLessons {
		t.Fatalf("fixture assumes maxSiblingLessons=%d, got %d", len(wantHead), maxSiblingLessons)
	}
	for _, l := range got[maxSiblingLessons:] {
		if l == "sibling oldest" {
			t.Errorf("sibling recall exceeded its share of %d slots: %v", maxSiblingLessons, got)
		}
		if !strings.HasPrefix(l, "keyword ") {
			t.Errorf("tail slot %q should hold a keyword match, got %v", l, got)
		}
	}
}

// TestRecallTicketLessonsDedupes collapses a takeaway that is both a sibling's and
// a keyword match onto one slot, keeping it in the sibling's leading position.
func TestRecallTicketLessonsDedupes(t *testing.T) {
	shared := "migration rollback needs the down step first"
	records := []lesson{
		lsn("COD-30", "migration", shared, "migration"),  // sibling
		lsn("COD-900", "migration", shared, "migration"), // same takeaway, unrelated ticket
		lsn("COD-901", "migration", "another migration takeaway", "migration"),
	}
	trk := &siblingTracker{parent: "COD-2", children: []string{"COD-30", "COD-31"}}
	p := siblingPipeline(t, "COD-31", "migration guard", trk, records...)

	got := p.recallTicketLessons(context.Background(), "COD-31")
	if len(got) != 2 {
		t.Fatalf("recall = %v, want the shared takeaway once plus the other match", got)
	}
	if got[0] != shared {
		t.Errorf("recall[0] = %q, want the sibling takeaway %q first", got[0], shared)
	}
	if got[1] == shared {
		t.Errorf("duplicate takeaway occupied two slots: %v", got)
	}
}

// TestRecallTicketLessonsWithoutEpicIsUnchanged: a top-level ticket — and a ticket
// whose hierarchy cannot be read — recalls exactly what it did before.
func TestRecallTicketLessonsWithoutEpicIsUnchanged(t *testing.T) {
	records := []lesson{
		lsn("COD-40", "migration", "run migrations before seeding", "migration"),
		lsn("COD-41", "ui", "wait for the selector before clicking", "ui"),
	}
	title := "migration ordering"

	cases := []struct {
		name string
		trk  tracker.Tracker
	}{
		{"top-level ticket", &siblingTracker{}},
		{"hierarchy unreadable", &siblingTracker{parent: "COD-3", subErr: errors.New("hub unreachable")}},
		{"epic with no other children", &siblingTracker{parent: "COD-3", children: []string{"COD-42"}}},
		{"tracker cannot report a parent", &fakeTracker{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := siblingPipeline(t, "COD-42", title, tc.trk, records...)
			want := relevantLessons(records, p.lessonQuery("COD-42"), maxInjectedLessons)
			got := p.recallTicketLessons(context.Background(), "COD-42")
			if len(got) != len(want) {
				t.Fatalf("recall = %v, want the keyword-only set %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("recall[%d] = %q, want %q", i, got[i], want[i])
				}
			}
		})
	}

	// Lessons disabled or ledger-less still injects nothing, epic or not.
	off := siblingPipeline(t, "COD-42", title, &siblingTracker{parent: "COD-3", children: []string{"COD-40"}}, records...)
	off.Lessons = false
	if got := off.recallTicketLessons(context.Background(), "COD-42"); got != nil {
		t.Errorf("disabled recall = %v, want nil", got)
	}
}

func TestRelevantLessonsFiltersByTagAndType(t *testing.T) {
	lessons := []lesson{
		lsn("COD-1", "migration", "run migrations before seeding to avoid foreign key errors", "migration"),
		lsn("COD-2", "test", "assertions on json shape need exact key casing", "test"),
		lsn("COD-3", "ui", "wait for selector before clicking in browser checks", "ui"),
	}

	got := relevantLessons(lessons, "migration foreign key constraint", maxInjectedLessons)
	if len(got) != 1 || got[0] != lessons[0].Lesson {
		t.Fatalf("migration query returned %v, want only the migration lesson", got)
	}

	got = relevantLessons(lessons, "ui selector browser", maxInjectedLessons)
	if len(got) != 1 || got[0] != lessons[2].Lesson {
		t.Fatalf("ui query returned %v, want only the ui lesson", got)
	}
}

func TestRelevantLessonsEmptyCases(t *testing.T) {
	lessons := []lesson{lsn("COD-1", "test", "something about tests", "test")}
	if got := relevantLessons(nil, "anything", 5); got != nil {
		t.Errorf("empty ledger should return nil, got %v", got)
	}
	if got := relevantLessons(lessons, "", 5); got != nil {
		t.Errorf("empty query should return nil, got %v", got)
	}
	if got := relevantLessons(lessons, "totally unrelated keywords", 5); got != nil {
		t.Errorf("no-match query should return nil, got %v", got)
	}
	if got := relevantLessons(lessons, "test", 0); got != nil {
		t.Errorf("max=0 should return nil, got %v", got)
	}
}

func TestRelevantLessonsCapsAndDedups(t *testing.T) {
	lessons := []lesson{
		lsn("COD-1", "test", "lesson A about test", "test"),
		lsn("COD-2", "test", "lesson B about test", "test"),
		lsn("COD-3", "test", "lesson C about test", "test"),
		lsn("COD-4", "test", "lesson A about test", "test"), // duplicate text of COD-1
	}
	got := relevantLessons(lessons, "test", 2)
	if len(got) != 2 {
		t.Fatalf("expected cap of 2, got %d: %v", len(got), got)
	}
	if got[0] == got[1] {
		t.Errorf("duplicate lessons should be collapsed, got %v", got)
	}
}

func TestRelevantLessonsRecencyTieBreak(t *testing.T) {
	// Same tag/type, no query word overlap → equal scores; the more recently
	// appended record (higher index) must come first.
	lessons := []lesson{
		lsn("COD-1", "migration", "older takeaway", "migration"),
		lsn("COD-2", "migration", "newer takeaway", "migration"),
	}
	got := relevantLessons(lessons, "migration", maxInjectedLessons)
	if len(got) != 2 {
		t.Fatalf("expected both lessons, got %v", got)
	}
	if got[0] != "newer takeaway" {
		t.Errorf("expected newer lesson first on a score tie, got %v", got)
	}
}

func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		name      string
		v         verdict
		wantType  string
		wantTag   string // a tag that must be present
		wantOther bool
	}{
		{"migration", verdict{Failures: []string{"migration rollback failed on users table"}}, "migration", "migration", false},
		{"test", verdict{Failures: []string{"failed asserting that two values are equal"}}, "test", "test", false},
		{"build", verdict{Failures: []string{"cannot find symbol Foo; undefined reference"}}, "build", "build", false},
		{"ui", verdict{Failures: []string{"selector .btn not found in browser"}}, "ui", "ui", false},
		{"unclassified", verdict{Summary: "something vague went wrong"}, "other", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ftype, tags := classifyFailure(tc.v)
			if ftype != tc.wantType {
				t.Errorf("failureType = %q, want %q", ftype, tc.wantType)
			}
			if tc.wantOther {
				if len(tags) != 0 {
					t.Errorf("unclassified failure should have no tags, got %v", tags)
				}
				return
			}
			found := false
			for _, tg := range tags {
				if tg == tc.wantTag {
					found = true
				}
			}
			if !found {
				t.Errorf("tags %v missing expected tag %q", tags, tc.wantTag)
			}
		})
	}
}

func TestNewLessonMechanicalText(t *testing.T) {
	v := verdict{Summary: "migration step crashed", Failures: []string{"migration rollback failed"}}
	repaired := newLesson("COD-9", "verify", "repair", lessonResultRepaired, v)
	if repaired.FailureType != "migration" {
		t.Errorf("failure type = %q, want migration", repaired.FailureType)
	}
	if repaired.Result != lessonResultRepaired || repaired.AttemptedFix != "repair" {
		t.Errorf("unexpected result/fix: %+v", repaired)
	}
	if repaired.Lesson == "" {
		t.Error("mechanical lesson text should not be empty")
	}

	quarantined := newLesson("COD-9", "verify", "repair+bugfix", lessonResultQuarantined, v)
	if quarantined.Lesson == repaired.Lesson {
		t.Error("repaired and quarantined lessons should read differently")
	}
}

func TestNewLessonCapsEvidence(t *testing.T) {
	var fails []string
	for i := 0; i < maxEvidenceLines+5; i++ {
		fails = append(fails, "failure line")
	}
	l := newLesson("COD-1", "verify", "repair", lessonResultQuarantined, verdict{Failures: fails})
	if len(l.Evidence) != maxEvidenceLines {
		t.Errorf("evidence length = %d, want capped at %d", len(l.Evidence), maxEvidenceLines)
	}
}

func TestAttemptLabel(t *testing.T) {
	tests := []struct {
		repairs, bugfixes int
		want              string
	}{
		{0, 0, "none"},
		{2, 0, "repair"},
		{0, 1, "bugfix"},
		{1, 1, "repair+bugfix"},
	}
	for _, tc := range tests {
		if got := attemptLabel(tc.repairs, tc.bugfixes); got != tc.want {
			t.Errorf("attemptLabel(%d,%d) = %q, want %q", tc.repairs, tc.bugfixes, got, tc.want)
		}
	}
}

func TestLessonsNotesEmptyWhenNoLessons(t *testing.T) {
	if s := buildLessonsNote(nil); s != "" {
		t.Errorf("buildLessonsNote(nil) = %q, want empty", s)
	}
	if s := verifyLessonsNote(nil); s != "" {
		t.Errorf("verifyLessonsNote(nil) = %q, want empty", s)
	}
	if s := repairLessonsNote(nil); s != "" {
		t.Errorf("repairLessonsNote(nil) = %q, want empty", s)
	}
	if s := buildLessonsNote([]string{"do the thing"}); s == "" {
		t.Error("buildLessonsNote with a lesson should be non-empty")
	}
}
