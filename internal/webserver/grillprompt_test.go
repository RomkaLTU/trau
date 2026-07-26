package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/attachfile"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/prompts"
)

// A grilling prompt points the agent at the local copy of the issue's screenshot
// and lists the file, so it can describe what the image shows while interviewing.
func TestGrillIssuePromptReferencesMaterializedFiles(t *testing.T) {
	files := []attachfile.File{
		{Ref: attachfile.Ref{ID: 1, Filename: "shot.png", MimeType: "image/png", Size: 2048, IsImage: true, SourceURL: "https://uploads.linear.app/abc/shot.png"}, Path: "/tmp/trau-attachments-COD-1/shot.png"},
	}
	body := "The toolbar breaks:\n\n![shot](https://uploads.linear.app/abc/shot.png)"

	for name, got := range map[string]string{
		"grillIssuePrompt":    grillIssuePrompt(prompts.Renderer{}, "COD-1", "Broken toolbar", body, "", files),
		"grillPregrillPrompt": grillPregrillPrompt(prompts.Renderer{}, "COD-1", "Broken toolbar", body, files),
	} {
		for _, want := range []string{
			"![shot](/tmp/trau-attachments-COD-1/shot.png)",
			"--- Attachments ---",
			"image/png",
			"read them",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: missing %q in:\n%s", name, want, got)
			}
		}
		if strings.Contains(got, "https://uploads.linear.app/abc/shot.png") {
			t.Errorf("%s: kept the unreachable tracker URL", name)
		}
	}
}

// An issue with no files renders exactly as it did before attachments existed.
func TestGrillIssuePromptWithoutAttachments(t *testing.T) {
	got := grillIssuePrompt(prompts.Renderer{}, "COD-1", "Broken toolbar", "Fix the toolbar.", "", nil)
	if strings.Contains(got, "--- Attachments ---") {
		t.Errorf("empty attachment list should render no section:\n%s", got)
	}
	if !strings.Contains(got, "Fix the toolbar.\n\n") {
		t.Errorf("description spacing changed:\n%s", got)
	}
	if empty := grillIssuePrompt(prompts.Renderer{}, "COD-1", "Broken toolbar", "", "", nil); !strings.Contains(empty, "(no description yet)\n\n") {
		t.Errorf("missing description placeholder:\n%s", empty)
	}
}

// A file that will not fetch is named as unavailable rather than silently dropped.
func TestGrillIssuePromptNotesUnavailableFile(t *testing.T) {
	files := []attachfile.File{
		{Ref: attachfile.Ref{ID: 1, Filename: "shot.png"}, Err: errors.New("hub unreachable")},
	}
	got := grillIssuePrompt(prompts.Renderer{}, "COD-1", "Broken toolbar", "See the shot.", "", files)
	if !strings.Contains(got, "shot.png unavailable:") {
		t.Errorf("missing the unavailable note:\n%s", got)
	}
}

// The first turn renders through the repo-scoped prompt catalog, so an edited body
// reaches the next interview: repo override beats global beats built-in default.
func TestGrillFirstPromptFollowsCatalogPrecedence(t *testing.T) {
	r, store, repo, _ := newGrillRunnerTest(t, grillStubScript)
	sess, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-1107"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { attachfile.Remove(grillAttachTicket(sess)) })

	ctx := context.Background()
	if got := r.firstPrompt(ctx, repo, sess); !strings.HasPrefix(got, "You are clarifying a software issue") {
		t.Fatalf("default interview body missing:\n%s", got)
	}

	overrides := r.srv.stores.Prompts()
	if err := overrides.Set("grill_issue", "", "GLOBAL interview of {{.ID}}: {{.Body}}"); err != nil {
		t.Fatalf("set global override: %v", err)
	}
	if got := r.firstPrompt(ctx, repo, sess); !strings.HasPrefix(got, "GLOBAL interview of COD-1107:") {
		t.Fatalf("global override ignored:\n%s", got)
	}

	if err := overrides.Set("grill_issue", repo.Root, "REPO interview of {{.ID}}: {{.Body}}"); err != nil {
		t.Fatalf("set repo override: %v", err)
	}
	if got := r.firstPrompt(ctx, repo, sess); !strings.HasPrefix(got, "REPO interview of COD-1107:") {
		t.Fatalf("repo override did not beat the global one:\n%s", got)
	}
}

// The three interview entries are separate catalog rows: editing the live one
// leaves the Ask-ahead and authoring first turns on their own bodies.
func TestGrillPromptVariantsAreIndependentlyEditable(t *testing.T) {
	r, store, repo, _ := newGrillRunnerTest(t, grillStubScript)
	if err := r.srv.stores.Prompts().Set("grill_issue", repo.Root, "LIVE {{.ID}}: {{.Body}}"); err != nil {
		t.Fatalf("set interview override: %v", err)
	}

	issue, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-1107"})
	if err != nil {
		t.Fatalf("create issue session: %v", err)
	}
	t.Cleanup(func() { attachfile.Remove(grillAttachTicket(issue)) })
	authoring, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root})
	if err != nil {
		t.Fatalf("create authoring session: %v", err)
	}
	t.Cleanup(func() { attachfile.Remove(grillAttachTicket(authoring)) })

	ctx := context.Background()
	r.srv.markPregrill(issue.ID)
	if got := r.firstPrompt(ctx, repo, issue); !strings.HasPrefix(got, "You are triaging a software issue ahead of time") {
		t.Fatalf("Ask-ahead first turn picked up the interview override:\n%s", got)
	}
	r.srv.clearPregrill(issue.ID)

	if got := r.firstPrompt(ctx, repo, authoring); !strings.HasPrefix(got, "You are helping the user turn a rough idea") {
		t.Fatalf("authoring first turn picked up the interview override:\n%s", got)
	}
	if got := r.firstPrompt(ctx, repo, issue); !strings.HasPrefix(got, "LIVE COD-1107:") {
		t.Fatalf("interview override not applied:\n%s", got)
	}
}

// A focus note on an issue-bound create opens the conversation and aims the first
// turn: it is stored as the user's opening message and the spawned prompt carries
// the section built from it. A create without one leaves both untouched.
func TestGrillIssueCreateHonoursFocusNote(t *testing.T) {
	r, store, repo, stubDir := newGrillRunnerTest(t, grillStubScript)
	r.srv.startGrill = r.launch
	ts := httptest.NewServer(r.srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(func() {
		attachfile.Remove("COD-1224")
		attachfile.Remove("COD-1225")
	})

	const focus = "Whether the collapse threshold should be configurable."
	focused := startIssueGrill(t, ts, repo.Name, "COD-1224", focus)
	waitForGrillChain(t, store, focused, "sid-one")

	prompt := lastArg(readNullArgs(t, filepath.Join(stubDir, "first.args")))
	for _, want := range []string{"The person starting this interview wants clarified:", focus} {
		if !strings.Contains(prompt, want) {
			t.Errorf("first-turn prompt missing %q:\n%s", want, prompt)
		}
	}

	msgs := grillThread(t, ts, focused)
	if len(msgs) == 0 {
		t.Fatal("focus note did not open the conversation")
	}
	if opening := msgs[0]; opening.Role != hubstore.GrillRoleUser || opening.Kind != hubstore.GrillKindInfo {
		t.Errorf("opening message = %s/%s, want the user's info turn", opening.Role, opening.Kind)
	} else if got := grillMessageText(string(opening.Payload)); got != focus {
		t.Errorf("opening message = %q, want the focus note", got)
	}

	bare := startIssueGrill(t, ts, repo.Name, "COD-1225", "")
	waitForGrillChain(t, store, bare, "sid-one")
	if prompt := lastArg(readNullArgs(t, filepath.Join(stubDir, "first.args"))); strings.Contains(prompt, "wants clarified") {
		t.Errorf("a create without a note rendered the focus section:\n%s", prompt)
	}
	if msgs := grillThread(t, ts, bare); len(msgs) != 0 {
		t.Errorf("a create without a note opened the conversation with %+v", msgs)
	}
}

func startIssueGrill(t *testing.T, ts *httptest.Server, repo, issue, idea string) int64 {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", GrillCreateRequest{IssueID: issue, Idea: idea})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", res.StatusCode)
	}
	var v GrillSessionView
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	sid, err := strconv.ParseInt(v.ID, 10, 64)
	if err != nil {
		t.Fatalf("session id %q: %v", v.ID, err)
	}
	return sid
}

func grillThread(t *testing.T, ts *httptest.Server, sid int64) []GrillMessageView {
	t.Helper()
	_, body := get(t, ts, APIPrefix+"/grill/"+strconv.FormatInt(sid, 10))
	var detail GrillDetailResponse
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	return detail.Messages
}
