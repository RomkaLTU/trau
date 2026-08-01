package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// probeOrg is a loopback Azure DevOps organization that counts every request it
// serves, so a QA probe can assert how many times a shared pull actually hit the API.
type probeOrg struct {
	mu       sync.Mutex
	wiql     []string
	batch    int32
	comments int32
	teamHits int32
	failOn   int // work-item id whose comment read fails; 0 disables
	srv      *httptest.Server
}

func newProbeOrg(t *testing.T) *probeOrg {
	t.Helper()
	p := &probeOrg{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/_apis/work/teamsettings/teamfieldvalues"):
			atomic.AddInt32(&p.teamHits, 1)
			team := strings.Split(strings.Trim(path, "/"), "/")[1]
			_, _ = fmt.Fprintf(w, `{"field":{"referenceName":"System.AreaPath"},`+
				`"values":[{"value":"Contoso\\%s","includeChildren":true}]}`, team)
		case strings.HasSuffix(path, "/wiql"):
			var sent struct {
				Query string `json:"query"`
			}
			_ = json.NewDecoder(r.Body).Decode(&sent)
			p.mu.Lock()
			p.wiql = append(p.wiql, sent.Query)
			p.mu.Unlock()
			_, _ = w.Write([]byte(`{"workItems":[{"id":7},{"id":9}]}`))
		case strings.Contains(path, "/comments"):
			atomic.AddInt32(&p.comments, 1)
			id := 0
			_, _ = fmt.Sscanf(path[strings.LastIndex(path, "workitems/")+len("workitems/"):], "%d", &id)
			if p.failOn != 0 && id == p.failOn {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"transient"}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"comments":[{"id":%d00,"text":"<p>note for %d</p>",`+
				`"createdBy":{"displayName":"Grace"},"createdDate":"2026-07-28T12:00:00Z",`+
				`"modifiedDate":"2026-07-28T12:05:00Z"}]}`, id, id)
		case r.URL.Query().Get("ids") != "":
			atomic.AddInt32(&p.batch, 1)
			_, _ = w.Write([]byte(probeBatch))
		default:
			if serveAzureWork(w, r) {
				return
			}
			t.Errorf("unrouted %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// Both work items report a comment, so the sweep owes a round-trip for each.
const probeBatch = `{"value":[
	{"id":7,"fields":{"System.Title":"Seven","System.State":"Active","System.TeamProject":"Contoso",
		"System.ChangedDate":"2026-07-30T11:22:33.44Z","System.CommentCount":1}},
	{"id":9,"fields":{"System.Title":"Nine","System.State":"Active","System.TeamProject":"Contoso",
		"System.ChangedDate":"2026-07-29T10:00:00Z","System.CommentCount":1}}]}`

func (p *probeOrg) reader(t *testing.T, area string, teams ...string) Reader {
	t.Helper()
	r, err := NewReader("azure", Config{
		Team: "Contoso", APIKey: "pat", BaseURL: p.srv.URL,
		AreaPath: area, BoardTeams: teams, ReadyLabel: "ready-for-agent",
	})
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return r
}

func (p *probeOrg) wiqlCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.wiql)
}

// AC 14 / fail_condition 10: two repos on one board + one scope must coalesce into a
// single WIQL query, batch read and comment sweep, while each still gets its own rows.
func TestProbeSharedScopeCoalesces(t *testing.T) {
	p := newProbeOrg(t)
	a, b := p.reader(t, "", "Platform"), p.reader(t, "", "Platform")

	var wg sync.WaitGroup
	results := make([][]SyncedIssue, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	for i, rd := range []Reader{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = rd.SyncPull(context.Background(), ProjectBinding{}, "")
		}()
	}
	close(start)
	wg.Wait()

	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("reader %d SyncPull: %v", i, errs[i])
		}
		if len(results[i]) != 2 {
			t.Fatalf("reader %d pulled %d issues, want 2", i, len(results[i]))
		}
	}
	t.Logf("wiql=%d batch=%d comments=%d teamfieldvalues=%d",
		p.wiqlCount(), p.batch, p.comments, p.teamHits)
	if got := p.wiqlCount(); got != 1 {
		t.Errorf("WIQL queries = %d, want 1 coalesced pull", got)
	}
	if p.batch != 1 {
		t.Errorf("batch reads = %d, want 1 coalesced read", p.batch)
	}
	if p.comments != 2 {
		t.Errorf("comment reads = %d, want 2 (one per work item, swept once)", p.comments)
	}
}

// AC 15 / fail_condition 9: differing scope must never coalesce.
func TestProbeDifferentScopeDoesNotCoalesce(t *testing.T) {
	p := newProbeOrg(t)
	a, b := p.reader(t, "", "Platform"), p.reader(t, "", "Payments")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i, rd := range []Reader{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = rd.SyncPull(context.Background(), ProjectBinding{}, "")
		}()
	}
	close(start)
	wg.Wait()

	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("reader %d SyncPull: %v", i, errs[i])
		}
	}
	t.Logf("wiql=%d queries: %v", p.wiqlCount(), p.wiql)
	if got := p.wiqlCount(); got != 2 {
		t.Errorf("WIQL queries = %d, want 2 independent pulls for two scopes", got)
	}
}

// AC 11: AZURE_TEAMS with AZURE_AREA_PATH narrows to the intersection.
func TestProbeAreaAndTeamsIntersect(t *testing.T) {
	p := newProbeOrg(t)
	if _, err := p.reader(t, `Contoso\Platform`, "Api").SyncPull(
		context.Background(), ProjectBinding{}, ""); err != nil {
		t.Fatalf("SyncPull: %v", err)
	}
	q := p.wiql[0]
	t.Logf("WIQL = %s", q)
	if !strings.Contains(q, `[System.AreaPath] UNDER 'Contoso\Platform'`) {
		t.Errorf("query drops the AZURE_AREA_PATH clause: %s", q)
	}
	if !strings.Contains(q, `([System.AreaPath] UNDER 'Contoso\Api')`) {
		t.Errorf("query drops the AZURE_TEAMS clause: %s", q)
	}
	if strings.Contains(q, " OR [System.AreaPath] UNDER 'Contoso\\Platform'") {
		t.Errorf("query unions rather than intersects: %s", q)
	}
}

// AC 18 / fail_condition 13: one failed comment read must cost that discussion only.
func TestProbeLostCommentKeepsTheTicket(t *testing.T) {
	p := newProbeOrg(t)
	p.failOn = 7
	pulled, err := p.reader(t, "", "Platform").SyncPull(context.Background(), ProjectBinding{}, "")
	if err != nil {
		t.Fatalf("SyncPull aborted on a lost discussion: %v", err)
	}
	if len(pulled) != 2 {
		t.Fatalf("pulled %d issues, want both", len(pulled))
	}
	byID := map[string]SyncedIssue{}
	for _, iss := range pulled {
		byID[iss.ID] = iss
	}
	if got := byID["7"]; got.Title != "Seven" || len(got.Comments) != 0 {
		t.Errorf("7 = %+v, want it mirrored with its other fields and no discussion", got)
	}
	if got := byID["9"]; len(got.Comments) != 1 {
		t.Errorf("9 comments = %d, want the sibling's discussion kept", len(got.Comments))
	}
}

// AC 13: the loop's Pick and ListEligible run against the same scope the hub mirrors.
func TestProbePickIsTeamScoped(t *testing.T) {
	p := newProbeOrg(t)
	az := &AzureDevOps{
		OrgURL: p.srv.URL, PAT: "pat", Project: "Contoso",
		AreaPath: `Contoso\Platform`, Teams: []string{"Api"},
		ReadyLabel: "ready-for-agent",
	}
	if _, err := az.ListEligible(context.Background(), Scope{Team: "Contoso"}); err != nil {
		t.Fatalf("ListEligible: %v", err)
	}
	q := p.wiql[0]
	t.Logf("eligible WIQL = %s", q)
	if !strings.Contains(q, `[System.AreaPath] UNDER 'Contoso\Platform'`) ||
		!strings.Contains(q, `([System.AreaPath] UNDER 'Contoso\Api')`) {
		t.Errorf("Pick/ListEligible is not scoped like the sync: %s", q)
	}
}
