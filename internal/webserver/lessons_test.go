package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// seedLessonsFile writes raw JSONL to the repo's runs/memory/lessons.jsonl,
// exactly as the loop appends it — so the fixture is the real on-disk shape, not
// a mock.
func seedLessonsFile(t *testing.T, runsDir, content string) {
	t.Helper()
	dir := filepath.Join(runsDir, "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lessons.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("seed lessons.jsonl: %v", err)
	}
}

func getLessons(t *testing.T, ts *httptest.Server, repo string) LessonsResponse {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/lessons")
	if err != nil {
		t.Fatalf("GET lessons: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out LessonsResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode lessons: %v", err)
	}
	return out
}

// TestLessonsPopulated is the fixture-driven contract test for a repo with a real
// ledger: the resource surfaces every well-formed record newest-first, carries
// the timestamp and context through, tolerates a legacy record with no timestamp,
// and drops the blank, malformed, and empty-lesson lines.
func TestLessonsPopulated(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	seedLessonsFile(t, runsDir, `{"ticket":"COD-50","phase":"verify","failure_type":"migration","attempted_fix":"repair","result":"repaired","lesson":"run migrations before seeding","tags":["migration"]}
not json at all

{"ticket":"COD-60","phase":"verify","failure_type":"build","lesson":""}
{broken
{"ticket":"COD-100","phase":"verify","failure_type":"test","attempted_fix":"repair","evidence":["failed asserting exact json key casing"],"result":"repaired","lesson":"assert exact json key casing","tags":["test","route"],"recorded_at":"2026-07-04T10:00:00Z"}
`)

	ts := instancesServer(t, home)
	out := getLessons(t, ts, "acme")

	if out.Repo != "acme" {
		t.Errorf("Repo = %q, want acme", out.Repo)
	}
	if len(out.Lessons) != 2 {
		t.Fatalf("lessons = %d, want 2 (malformed/blank/empty dropped): %+v", len(out.Lessons), out.Lessons)
	}

	newest := out.Lessons[0]
	if newest.Ticket != "COD-100" {
		t.Errorf("first lesson = %q, want newest COD-100 first", newest.Ticket)
	}
	if newest.RecordedAt != "2026-07-04T10:00:00Z" {
		t.Errorf("recorded_at = %q, want the ledger timestamp carried through", newest.RecordedAt)
	}
	if newest.FailureType != "test" || newest.AttemptedFix != "repair" || newest.Result != "repaired" {
		t.Errorf("context not carried through: %+v", newest)
	}
	if len(newest.Evidence) != 1 || len(newest.Tags) != 2 {
		t.Errorf("evidence/tags not carried through: %+v", newest)
	}

	legacy := out.Lessons[1]
	if legacy.Ticket != "COD-50" {
		t.Errorf("second lesson = %q, want COD-50", legacy.Ticket)
	}
	if legacy.RecordedAt != "" {
		t.Errorf("legacy record recorded_at = %q, want empty", legacy.RecordedAt)
	}
	if legacy.Lesson != "run migrations before seeding" {
		t.Errorf("legacy lesson = %q, want it carried through", legacy.Lesson)
	}
}

// TestLessonsMissingFileEmpty covers the empty state: a repo the loop has taught
// nothing yet has no ledger, and the resource is a 200 with an empty list — not an
// error.
func TestLessonsMissingFileEmpty(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")

	ts := instancesServer(t, home)
	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/lessons")
	if err != nil {
		t.Fatalf("GET lessons: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	var out LessonsResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode lessons: %v", err)
	}
	if out.Repo != "acme" {
		t.Errorf("Repo = %q, want acme", out.Repo)
	}
	if out.Lessons == nil || len(out.Lessons) != 0 {
		t.Errorf("lessons = %+v, want an empty (non-null) list", out.Lessons)
	}
}

// TestLessonsUnknownRepo404 covers the miss: a repo the hub never saw is a JSON
// 404, not the SPA shell.
func TestLessonsUnknownRepo404(t *testing.T) {
	ts := instancesServer(t, t.TempDir())
	res, err := http.Get(ts.URL + APIPrefix + "/repos/ghost/lessons")
	if err != nil {
		t.Fatalf("GET lessons: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

// TestLessonsRejectsNonGET keeps the resource read-only.
func TestLessonsRejectsNonGET(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	res, err := http.Post(ts.URL+APIPrefix+"/repos/acme/lessons", "application/json", nil)
	if err != nil {
		t.Fatalf("POST lessons: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", res.StatusCode)
	}
}
