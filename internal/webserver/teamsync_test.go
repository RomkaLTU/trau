package webserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/teamsync"
)

// teamPeer is one developer's side of a shared repo: an isolated TRAU_HOME with
// its own hub, and a clone of the shared remote carrying their git identity.
type teamPeer struct {
	ts     *httptest.Server
	srv    *Server
	stores *hubstore.Stores
	root   string
	name   string
}

// newTeamFixture builds one bare remote plus a hub-and-clone per named developer,
// each hub isolated from the others' databases — the two-machine shape team sync
// actually runs in.
func newTeamFixture(t *testing.T, teamSync bool, names ...string) (remote string, peers map[string]*teamPeer) {
	t.Helper()
	// The hub resolves the user config layer from $HOME; point it at a scratch
	// directory so the developer's own ~/.trau.ini cannot reach the fixture.
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	gitRun(t, root, "init", "--bare", "-q", remote)

	peers = map[string]*teamPeer{}
	for _, name := range names {
		repoRoot := filepath.Join(t.TempDir(), name)
		gitRun(t, root, "clone", "-q", remote, repoRoot)
		gitRun(t, repoRoot, "config", "user.name", strings.ToUpper(name[:1])+name[1:])
		gitRun(t, repoRoot, "config", "user.email", name+"@example.com")
		if teamSync {
			writeTeamConfig(t, repoRoot, "TEAM_SYNC=1\n")
		}

		home := t.TempDir()
		stores := testStoresAt(t, home)
		if err := stores.Registrations().Remember([]registry.Repo{{
			Name:    name,
			Root:    repoRoot,
			RunsDir: filepath.Join(repoRoot, ".trau", "runs"),
		}}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		s := New("1.2.3", "127.0.0.1", "", nil, false, stores)
		s.home = home
		ts := httptest.NewServer(s.Handler())
		t.Cleanup(ts.Close)
		peers[name] = &teamPeer{ts: ts, srv: s, stores: stores, root: repoRoot, name: name}
	}
	return remote, peers
}

// TestTeamSyncSharesLessonsBetweenMachines is the end-to-end contract: a lesson
// recorded on one machine reaches the other machine's lessons resource — the same
// resource prompt-injection recall reads — attributed to the recorder's git name,
// while the reader's own records still say "Me".
func TestTeamSyncSharesLessonsBetweenMachines(t *testing.T) {
	remote, peers := newTeamFixture(t, true, "ada", "grace")
	ada, grace := peers["ada"], peers["grace"]

	postLesson(t, grace.ts, "grace", LessonView{Lesson: "grace's own takeaway", RecordedAt: "2026-07-26T09:00:00Z"})
	postLesson(t, ada.ts, "ada", LessonView{
		Lesson:      "run migrations before seeding",
		Ticket:      "COD-50",
		FailureType: "migration",
		RecordedAt:  "2026-07-26T10:00:00Z",
	})

	// Recording a lesson triggers the publish, so the ref appears without any
	// manual gesture.
	waitForTeamRefs(t, remote, 1)

	syncNow(t, grace.ts, "grace")

	out := getLessons(t, grace.ts, "grace")
	byText := map[string]LessonView{}
	for _, l := range out.Lessons {
		byText[l.Lesson] = l
	}

	theirs, ok := byText["run migrations before seeding"]
	if !ok {
		t.Fatalf("lessons = %+v, want the teammate's record folded in", out.Lessons)
	}
	if theirs.Me {
		t.Error("teammate record marked Me")
	}
	if theirs.Author != "Ada" {
		t.Errorf("author = %q, want the recorder's git name", theirs.Author)
	}
	if theirs.Ticket != "COD-50" || theirs.FailureType != "migration" {
		t.Errorf("teammate record lost context: %+v", theirs)
	}

	mine, ok := byText["grace's own takeaway"]
	if !ok {
		t.Fatalf("lessons = %+v, want the local record kept", out.Lessons)
	}
	if !mine.Me || mine.Author != "" {
		t.Errorf("local record = %+v, want Me with no author chip", mine)
	}

	// The newest record leads, and sync never re-exports what it read.
	if out.Lessons[0].Lesson != "run migrations before seeding" {
		t.Errorf("order = %q first, want most recent first", out.Lessons[0].Lesson)
	}
	if records, err := ada.stores.TeamSync().Records(ada.root); err != nil || len(records) != 0 {
		t.Errorf("records on the recorder = %+v (err %v), want none until it fetches", records, err)
	}
}

// TestTeamSyncCollapsesKicksDuringAPass pins the coalescing lifecycle triggers
// rely on: lessons recorded while a pass is under way ride the one pass queued
// behind it instead of each publishing their own.
func TestTeamSyncCollapsesKicksDuringAPass(t *testing.T) {
	pushes := traceGit(t)
	remote, peers := newTeamFixture(t, true, "ada")
	ada := peers["ada"]

	lock := ada.srv.team.repoLock(ada.root)
	lock.Lock()
	for i := range 5 {
		postLesson(t, ada.ts, "ada", LessonView{
			Lesson:     fmt.Sprintf("takeaway %d", i),
			RecordedAt: "2026-07-26T10:00:00Z",
		})
	}
	lock.Unlock()

	waitForTeamRefs(t, remote, 1)
	// A redundant pass would be queued behind the one that just finished, so give
	// it room to land before reading the count.
	time.Sleep(500 * time.Millisecond)

	if n := pushes(); n != 1 {
		t.Errorf("pushes = %d, want the five kicks to publish once", n)
	}
}

// TestTeamSyncOffCreatesNoRefs pins the default: with the toggle off nothing is
// fetched, pushed, or created, and the lessons resource is the local ledger alone.
func TestTeamSyncOffCreatesNoRefs(t *testing.T) {
	remote, peers := newTeamFixture(t, false, "ada")
	ada := peers["ada"]

	postLesson(t, ada.ts, "ada", LessonView{Lesson: "stays home", RecordedAt: "2026-07-26T10:00:00Z"})
	syncNow(t, ada.ts, "ada")

	if refs := gitRun(t, remote, "for-each-ref", "--format=%(refname)", "refs/trau/"); refs != "" {
		t.Errorf("remote refs = %q, want none with TEAM_SYNC off", refs)
	}
	if refs := gitRun(t, ada.root, "for-each-ref", "--format=%(refname)", "refs/trau/"); refs != "" {
		t.Errorf("local refs = %q, want none with TEAM_SYNC off", refs)
	}

	out := getLessons(t, ada.ts, "ada")
	if len(out.Lessons) != 1 || out.Lessons[0].Lesson != "stays home" {
		t.Fatalf("lessons = %+v, want the local ledger unchanged", out.Lessons)
	}
	if st, err := ada.stores.TeamSync().State(ada.root); err != nil || st != (hubstore.TeamSyncState{}) {
		t.Errorf("state = %+v (err %v), want no sync bookkeeping", st, err)
	}
}

// TestTeamSyncPushFailureIsRecordedNotRaised pins the failure posture: a remote
// that refuses the push leaves the lesson resource intact and shows up only as the
// repo's last error.
func TestTeamSyncPushFailureIsRecordedNotRaised(t *testing.T) {
	_, peers := newTeamFixture(t, true, "ada")
	ada := peers["ada"]
	gitRun(t, ada.root, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	postLesson(t, ada.ts, "ada", LessonView{Lesson: "still recorded", RecordedAt: "2026-07-26T10:00:00Z"})
	syncNow(t, ada.ts, "ada")

	out := getLessons(t, ada.ts, "ada")
	if len(out.Lessons) != 1 || !out.Lessons[0].Me {
		t.Fatalf("lessons = %+v, want the local record untouched", out.Lessons)
	}

	st, err := ada.stores.TeamSync().State(ada.root)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.LastError == "" {
		t.Error("last error is empty, want the refused push recorded")
	}
	if st.LastSyncAt == "" {
		t.Error("last sync time is empty, want the attempt recorded")
	}
}

// TestTeamSyncUnavailableWithoutRemote pins the settings-surface copy: a repo with
// no git remote cannot opt in, so the toggle is disabled and the write is refused.
func TestTeamSyncUnavailableWithoutRemote(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "solo")
	gitRun(t, t.TempDir(), "init", "-q", root)

	stores := testStoresAt(t, home)
	if err := stores.Registrations().Remember([]registry.Repo{{Name: "solo", Root: root}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	s := New("1.2.3", "127.0.0.1", "", nil, false, stores)
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res, body := get(t, ts, APIPrefix+"/repos/solo/config")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var cfg ConfigResponse
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	var view ConfigKeyView
	for _, k := range cfg.Keys {
		if k.Key == "TEAM_SYNC" {
			view = k
		}
	}
	if view.Key == "" {
		t.Fatal("TEAM_SYNC missing from the settings catalog")
	}
	if view.Editable {
		t.Error("TEAM_SYNC editable on a repo with no remote")
	}
	if !strings.Contains(view.DisabledReason, "no git remote") {
		t.Errorf("disabled reason = %q, want it to name the missing remote", view.DisabledReason)
	}

	req, err := http.NewRequest(http.MethodPut, ts.URL+APIPrefix+"/repos/solo/config",
		bytes.NewReader([]byte(`{"key":"TEAM_SYNC","value":"1","layer":"project"}`)))
	if err != nil {
		t.Fatalf("new PUT: %v", err)
	}
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT config: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a repo that cannot sync", res.StatusCode)
	}
}

func TestMergeLessonViews(t *testing.T) {
	local := []LessonView{
		{Lesson: "shared takeaway", RecordedAt: "2026-07-26T09:00:00Z"},
		{Lesson: "mine only", RecordedAt: "2026-07-26T11:00:00Z"},
	}
	teammates := []LessonView{
		{Lesson: "  shared takeaway  ", RecordedAt: "2026-07-26T12:00:00Z", Author: "Ada"},
		{Lesson: "theirs only", RecordedAt: "2026-07-26T10:00:00Z", Author: "Ada"},
		{Lesson: "theirs only", RecordedAt: "2026-07-26T08:00:00Z", Author: "Grace"},
		{Lesson: "   ", RecordedAt: "2026-07-26T13:00:00Z", Author: "Grace"},
	}

	got := mergeLessonViews(local, teammates)

	want := []string{"mine only", "theirs only", "shared takeaway"}
	if len(got) != len(want) {
		t.Fatalf("merged = %+v, want %d records", got, len(want))
	}
	for i, lesson := range want {
		if got[i].Lesson != lesson {
			t.Errorf("merged[%d] = %q, want %q", i, got[i].Lesson, lesson)
		}
	}
	if !got[0].Me || !got[2].Me {
		t.Error("local records lost their Me marker")
	}
	if got[1].Author != "Ada" {
		t.Errorf("dedup kept %q, want the first teammate record", got[1].Author)
	}
	if got[2].Author != "" {
		t.Errorf("local record picked up author %q, want the local one to win", got[2].Author)
	}
}

func postLesson(t *testing.T, ts *httptest.Server, repo string, lv LessonView) {
	t.Helper()
	body, err := json.Marshal(lv)
	if err != nil {
		t.Fatalf("marshal lesson: %v", err)
	}
	res, err := http.Post(ts.URL+APIPrefix+"/repos/"+repo+"/lessons", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST lesson: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST lesson status = %d, want 200", res.StatusCode)
	}
}

func syncNow(t *testing.T, ts *httptest.Server, repo string) TeamSyncStatus {
	t.Helper()
	res, err := http.Post(ts.URL+APIPrefix+"/repos/"+repo+"/team-sync", "application/json", nil)
	if err != nil {
		t.Fatalf("POST team-sync: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST team-sync status = %d, want 200", res.StatusCode)
	}
	var out TeamSyncStatus
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode team-sync: %v", err)
	}
	return out
}

// waitForTeamRefs blocks until the remote carries want writer refs, so a test can
// depend on the publish a lifecycle trigger kicked off in the background.
func waitForTeamRefs(t *testing.T, remote string, want int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		out := gitRun(t, remote, "for-each-ref", "--format=%(refname)", teamsync.RefPrefix)
		if len(strings.Fields(out)) >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("remote refs = %q, want %d under %s", out, want, teamsync.RefPrefix)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// traceGit puts a logging shim ahead of the real git on PATH and reports how many
// pushes have run through it, so a test can count the passes a burst of triggers
// actually made rather than the refs they left behind.
func traceGit(t *testing.T) func() int {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locate git: %v", err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "git.log")
	shim := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatalf("write git shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() int {
		out, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("read git trace: %v", err)
		}
		return strings.Count(string(out), " push ")
	}
}

func writeTeamConfig(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".trau.ini"), []byte(content), 0o644); err != nil {
		t.Fatalf("write .trau.ini: %v", err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, errBuf.String())
	}
	return strings.TrimSpace(out.String())
}
