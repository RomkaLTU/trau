package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

func TestMapLinearGroup(t *testing.T) {
	cases := map[string]StatusGroup{
		"triage":    StatusGroupBacklog,
		"backlog":   StatusGroupBacklog,
		"unstarted": StatusGroupUnstarted,
		"started":   StatusGroupStarted,
		"completed": StatusGroupDone,
		"canceled":  StatusGroupCanceled,
		"weird":     StatusGroupUnknown,
	}
	for typ, want := range cases {
		if got := mapLinearGroup(typ); got != want {
			t.Errorf("mapLinearGroup(%q) = %q, want %q", typ, got, want)
		}
	}
}

func TestMapJiraGroup(t *testing.T) {
	cases := []struct {
		category, resolution string
		want                 StatusGroup
	}{
		{"new", "", StatusGroupUnstarted},
		{"indeterminate", "", StatusGroupStarted},
		{"done", "", StatusGroupDone},
		{"done", "Done", StatusGroupDone},
		{"done", "Won't Do", StatusGroupCanceled},
		{"done", "Duplicate", StatusGroupCanceled},
		{"mystery", "", StatusGroupUnknown},
	}
	for _, tc := range cases {
		if got := mapJiraGroup(tc.category, tc.resolution); got != tc.want {
			t.Errorf("mapJiraGroup(%q, %q) = %q, want %q", tc.category, tc.resolution, got, tc.want)
		}
	}
}

func TestNewReaderUnavailableWithoutCredentials(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		cfg      Config
	}{
		{"linear without key", "linear", Config{Team: "COD"}},
		{"jira missing token", "jira", Config{Team: "PROJ", BaseURL: "https://acme.atlassian.net", Email: "me@acme.com"}},
		{"github has no direct read API", "github", Config{Team: "acme/app"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewReader(tc.provider, tc.cfg); !errors.Is(err, ErrReaderUnavailable) {
				t.Errorf("NewReader(%s) err = %v, want ErrReaderUnavailable", tc.provider, err)
			}
		})
	}
}

func TestJiraReaderResolveBindingNoProjectKey(t *testing.T) {
	r := &jiraReader{client: jiraapi.New("https://acme.atlassian.net", "me@acme.com", "tok")}

	_, err := r.ResolveBinding(context.Background())
	if !errors.Is(err, ErrNoProjectKey) {
		t.Fatalf("ResolveBinding err = %v, want ErrNoProjectKey", err)
	}
	if errors.Is(err, ErrReaderUnavailable) {
		t.Fatalf("ResolveBinding err = %v, must not read as no credentials", err)
	}
	if got := err.Error(); strings.Contains(got, "credentials") || !strings.Contains(got, "LINEAR_TEAM") {
		t.Fatalf("ResolveBinding err = %q, want it to name LINEAR_TEAM and not mention credentials", got)
	}
}

func TestLinearReaderBacklogMapsAndFiltersProject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "query Teams"):
			_, _ = io.WriteString(w, `{"data":{"teams":{"nodes":[{"id":"team-1","key":"COD","name":"Codesome"}]}}}`)
		case strings.Contains(req.Query, "query Backlog"):
			_, _ = io.WriteString(w, `{"data":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
				{"identifier":"COD-10","title":"Epic","state":{"name":"Backlog","type":"backlog"},"project":{"name":"Trau Web"},"parent":{"identifier":""},"labels":{"nodes":[{"id":"l0","name":"epic"}]},"children":{"nodes":[{"id":"c1"}]}},
				{"identifier":"COD-11","title":"Child","state":{"name":"Todo","type":"unstarted"},"project":{"name":"Trau Web"},"parent":{"identifier":"COD-10"},"labels":{"nodes":[{"id":"l1","name":"ready-for-agent"}]},"children":{"nodes":[]}},
				{"identifier":"COD-12","title":"Elsewhere","state":{"name":"In Progress","type":"started"},"project":{"name":"Other"},"parent":{"identifier":""},"labels":{"nodes":[]},"children":{"nodes":[]}},
				{"identifier":"COD-13","title":"Shipped","state":{"name":"Done","type":"completed"},"project":{"name":"Trau Web"},"parent":{"identifier":""},"labels":{"nodes":[]},"children":{"nodes":[]}}
			]}}}`)
		default:
			t.Errorf("unexpected query: %s", req.Query)
		}
	}))
	t.Cleanup(srv.Close)

	c := linearapi.New("lin_key")
	c.Endpoint = srv.URL
	r := &linearReader{client: c, team: "COD", project: "Trau Web", readyLabel: "ready-for-agent"}

	items, err := r.Backlog(context.Background())
	if err != nil {
		t.Fatalf("Backlog: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3 (COD-12 filtered out by project)", len(items))
	}

	epic := items[0]
	if epic.ID != "COD-10" || epic.Group != StatusGroupBacklog || !epic.HasChildren || epic.Ready {
		t.Errorf("epic = %+v, want COD-10 backlog epic, not ready", epic)
	}
	child := items[1]
	if child.ID != "COD-11" || child.Group != StatusGroupUnstarted || child.Parent != "COD-10" || !child.Ready {
		t.Errorf("child = %+v, want COD-11 unstarted, parent COD-10, ready", child)
	}
	if child.Status != "Todo" {
		t.Errorf("child display status = %q, want the raw state name Todo", child.Status)
	}
	if items[2].ID != "COD-13" || items[2].Group != StatusGroupDone {
		t.Errorf("items[2] = %+v, want COD-13 done", items[2])
	}
}

func TestJiraReaderBacklogMaps(t *testing.T) {
	const payload = `{"issues":[
		{"key":"PROJ-1","fields":{
			"summary":"Epic","status":{"name":"To Do","statusCategory":{"key":"new"}},
			"issuetype":{"hierarchyLevel":1},"labels":["epic"]
		}},
		{"key":"PROJ-2","fields":{
			"summary":"Child","status":{"name":"To Do","statusCategory":{"key":"new"}},
			"issuetype":{"hierarchyLevel":0},"labels":["ready-for-agent"],"parent":{"key":"PROJ-1"}
		}},
		{"key":"PROJ-3","fields":{
			"summary":"Dropped","status":{"name":"Closed","statusCategory":{"key":"done"}},
			"issuetype":{"hierarchyLevel":0},"resolution":{"name":"Duplicate"}
		}}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)

	r := &jiraReader{client: jiraapi.New(srv.URL, "me@acme.com", "tok"), project: "PROJ", readyLabel: "ready-for-agent"}
	items, err := r.Backlog(context.Background())
	if err != nil {
		t.Fatalf("Backlog: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	if items[0].ID != "PROJ-1" || !items[0].HasChildren || items[0].Group != StatusGroupUnstarted {
		t.Errorf("items[0] = %+v, want the PROJ-1 epic, unstarted", items[0])
	}
	if items[1].ID != "PROJ-2" || items[1].Parent != "PROJ-1" || !items[1].Ready {
		t.Errorf("items[1] = %+v, want ready child parented to PROJ-1", items[1])
	}
	if items[2].Group != StatusGroupCanceled || items[2].Status != "Closed" {
		t.Errorf("items[2] = %+v, want a canceled (duplicate) done issue", items[2])
	}
}
