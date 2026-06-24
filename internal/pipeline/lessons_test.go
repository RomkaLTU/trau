package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// newLesson-style helper for terser table rows.
func lsn(ticket, ftype, text string, tags ...string) lesson {
	return lesson{Ticket: ticket, FailureType: ftype, Lesson: text, Tags: tags}
}

func TestAppendLessonRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := &Pipeline{RunsDir: dir, Lessons: true}

	want := []lesson{
		lsn("COD-1", "migration", "run migrations before seeding", "migration"),
		lsn("COD-2", "test", "assert exact json key casing", "test"),
	}
	for _, l := range want {
		p.appendLesson(l)
	}

	// The ledger lands at the documented runs/memory/lessons.jsonl path.
	path := filepath.Join(dir, "memory", "lessons.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("ledger not written at %s: %v", path, err)
	}

	got := readLessons(p.lessonsPath())
	if len(got) != len(want) {
		t.Fatalf("readLessons returned %d records, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Ticket != want[i].Ticket || got[i].Lesson != want[i].Lesson {
			t.Errorf("record %d = %+v, want ticket=%s lesson=%q", i, got[i], want[i].Ticket, want[i].Lesson)
		}
	}
}

func TestAppendLessonDisabledIsNoOp(t *testing.T) {
	dir := t.TempDir()
	p := &Pipeline{RunsDir: dir, Lessons: false}
	p.appendLesson(lsn("COD-1", "test", "nope"))
	if got := readLessons(p.lessonsPath()); len(got) != 0 {
		t.Fatalf("disabled pipeline wrote %d records, want 0", len(got))
	}
}

func TestReadLessonsSkipsMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lessons.jsonl")
	content := `{"ticket":"COD-1","lesson":"good one","failure_type":"test","tags":["test"]}
this is not json at all
{"ticket":"COD-2","lesson":"second good","failure_type":"ui"}

{"ticket":"COD-3","lesson":"","failure_type":"build"}
{broken json
{"ticket":"COD-4","lesson":"third good"}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got := readLessons(path)
	// Skips: the non-JSON line, the blank line, the empty-lesson record, and the
	// broken-JSON line — keeping the three with real lessons.
	if len(got) != 3 {
		t.Fatalf("got %d records, want 3 (malformed/empty skipped): %+v", len(got), got)
	}
	wantTickets := []string{"COD-1", "COD-2", "COD-4"}
	for i, w := range wantTickets {
		if got[i].Ticket != w {
			t.Errorf("record %d ticket = %s, want %s", i, got[i].Ticket, w)
		}
	}
}

func TestReadLessonsMissingFile(t *testing.T) {
	if got := readLessons(filepath.Join(t.TempDir(), "nope.jsonl")); got != nil {
		t.Fatalf("missing ledger should read as nil, got %+v", got)
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
