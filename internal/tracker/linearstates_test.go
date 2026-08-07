package tracker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

// LINEAR_BOARD_STATES is an overlay, not the exhaustive mapping its Azure
// counterpart is: a state it names takes the mapped section, and a state it does
// not keeps whatever its Linear state type derives — including a state created
// after the mapping was written, which is the whole reason for the difference.
func TestLinearBoardStatesOverlaysTheTypeGrouping(t *testing.T) {
	mapping := parseLinearBoardStates("Triage=unstarted, ready for qa =started,Icebox=backlog")
	cases := []struct {
		name         string
		state, typ   string
		want         StatusGroup
		wantOverride bool
	}{
		{name: "mapped state", state: "Triage", typ: "triage", want: StatusGroupUnstarted, wantOverride: true},
		{name: "mapped case- and space-insensitively", state: "  Ready For QA  ", typ: "started", want: StatusGroupStarted, wantOverride: true},
		{name: "unlisted state keeps its type", state: "In Progress", typ: "started", want: StatusGroupStarted},
		{name: "state added later keeps its type, never unknown", state: "Paused", typ: "unstarted", want: StatusGroupUnstarted},
		{name: "unlisted completed state", state: "Shipped", typ: "completed", want: StatusGroupDone},
		{name: "the duplicate type Linear carries", state: "Duplicate", typ: "duplicate", want: StatusGroupCanceled},
		{name: "a type trau does not know still lands in a section", state: "Odd", typ: "invented", want: StatusGroupBacklog},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapping.group(tc.state, tc.typ); got != tc.want {
				t.Errorf("group(%q, %q) = %q, want %q", tc.state, tc.typ, got, tc.want)
			}
			if _, ok := mapping.override(tc.state); ok != tc.wantOverride {
				t.Errorf("override(%q) mapped = %v, want %v", tc.state, ok, tc.wantOverride)
			}
		})
	}
}

// An empty or unparseable spec leaves grouping exactly where it was, so deleting
// the key restores pure type-derived grouping.
func TestLinearBoardStatesEmptyLeavesTypeGrouping(t *testing.T) {
	for _, spec := range []string{"", "   ", ",,", "Triage=nowhere", "=started"} {
		mapping := parseLinearBoardStates(spec)
		if _, ok := mapping.override("Triage"); ok {
			t.Errorf("parseLinearBoardStates(%q) claims an override for Triage", spec)
		}
		if got := mapping.group("Triage", "triage"); got != StatusGroupBacklog {
			t.Errorf("parseLinearBoardStates(%q): group = %q, want %q", spec, got, StatusGroupBacklog)
		}
	}
}

// Two repos on one Linear team share a pull only when they group its states the
// same way, so the mapping's canonical key has to differ whenever the mapping
// does — and match whatever order or casing it was written in.
func TestLinearScopeKeyDivergesWithTheMapping(t *testing.T) {
	binding := ProjectBinding{TeamID: "team-1", ProjectID: "proj-1"}
	key := func(spec string) string {
		r := &linearReader{boardStates: parseLinearBoardStates(spec)}
		return r.scopeKey("pull", binding, "")
	}

	same := key("Triage=unstarted,Ready for QA=started")
	reordered := key("ready for qa=started , Triage=unstarted")
	if same != reordered {
		t.Errorf("the same mapping written twice keys differently:\n%q\n%q", same, reordered)
	}
	if other := key("Triage=backlog,Ready for QA=started"); other == same {
		t.Errorf("two different mappings share the pull key %q", same)
	}
	if unmapped := key(""); unmapped == same {
		t.Errorf("an unmapped repo shares the pull key of a mapped one (%q)", same)
	}
}

// linearStatesFake answers the two queries a Linear reader makes for a team's
// backlog, with a workflow whose Ready for QA state Linear types as started and a
// Duplicate state typed duplicate — the type Linear's own docs leave out.
func linearStatesFake(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		q := string(body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(q, "query Teams"):
			_, _ = io.WriteString(w, `{"data":{"teams":{"nodes":[{"id":"team-1","key":"COD","name":"Codes"}]}}}`)
		case strings.Contains(q, "query Backlog"):
			_, _ = io.WriteString(w, `{"data":{"issues":{"pageInfo":{"hasNextPage":false},"nodes":[
				{"identifier":"COD-1","title":"queued","state":{"name":"Ready for QA","type":"started"}},
				{"identifier":"COD-2","title":"running","state":{"name":"In Progress","type":"started"}},
				{"identifier":"COD-3","title":"parked","state":{"name":"Icebox","type":"backlog"}},
				{"identifier":"COD-4","title":"redundant","state":{"name":"Duplicate","type":"duplicate"}}
			]}}}`)
		case strings.Contains(q, "query WorkflowStates"):
			_, _ = io.WriteString(w, `{"data":{"workflowStates":{"nodes":[
				{"id":"s1","name":"Icebox","type":"backlog"},
				{"id":"s2","name":"In Progress","type":"started"},
				{"id":"s3","name":"Ready for QA","type":"started"},
				{"id":"s4","name":"Live","type":"completed"},
				{"id":"s5","name":"Duplicate","type":"duplicate"}
			]}}}`)
		default:
			t.Errorf("unexpected GraphQL query: %s", q)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func linearStatesReader(t *testing.T, spec string) *linearReader {
	t.Helper()
	client := linearapi.New("lin_key")
	client.Endpoint = linearStatesFake(t).URL
	return &linearReader{client: client, team: "COD", boardStates: parseLinearBoardStates(spec)}
}

// The board a repo reads follows its overlay: the one state it remaps moves, and
// every other row stays exactly where its type put it.
func TestLinearReaderBacklogAppliesTheOverlay(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want map[string]StatusGroup
	}{
		{
			name: "no mapping keeps pure type grouping",
			want: map[string]StatusGroup{
				"COD-1": StatusGroupStarted,
				"COD-2": StatusGroupStarted,
				"COD-3": StatusGroupBacklog,
				"COD-4": StatusGroupCanceled,
			},
		},
		{
			name: "one override moves one state and leaves the rest alone",
			spec: "Ready for QA=done",
			want: map[string]StatusGroup{
				"COD-1": StatusGroupDone,
				"COD-2": StatusGroupStarted,
				"COD-3": StatusGroupBacklog,
				"COD-4": StatusGroupCanceled,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, err := linearStatesReader(t, tc.spec).Backlog(context.Background())
			if err != nil {
				t.Fatalf("Backlog error: %v", err)
			}
			if len(items) != len(tc.want) {
				t.Fatalf("read %d rows, want %d", len(items), len(tc.want))
			}
			for _, item := range items {
				if got := item.Group; got != tc.want[item.ID] {
					t.Errorf("%s (%s) grouped as %q, want %q", item.ID, item.Status, got, tc.want[item.ID])
				}
			}
		})
	}
}

// Reconcile reads the same overlay the board groups by, so a --status pass and
// the board never disagree about the same ticket. A state the overlay does not
// name keeps its type's own reading.
func TestLinearIssueStatusFollowsTheOverlay(t *testing.T) {
	cases := []struct {
		name  string
		spec  string
		state string
		typ   string
		want  IssueStatus
	}{
		{name: "overridden to done", spec: "Ready for QA=done", state: "Ready for QA", typ: "started", want: StatusDone},
		{name: "overridden to backlog", spec: "Ready for QA=backlog", state: "Ready for QA", typ: "started", want: StatusOpen},
		{name: "unlisted state keeps its type", spec: "Icebox=backlog", state: "Ready for QA", typ: "started", want: StatusStarted},
		{name: "no mapping at all", state: "Ready for QA", typ: "started", want: StatusStarted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), "query Issue") {
					t.Errorf("unexpected GraphQL query: %s", body)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"data":{"issues":{"nodes":[{"id":"iss-1","identifier":"COD-7",
					"state":{"name":"`+tc.state+`","type":"`+tc.typ+`"}}]}}}`)
			}))
			defer srv.Close()

			l := &Linear{
				Team:        "COD",
				APIKey:      "lin_key",
				endpoint:    srv.URL,
				boardStates: parseLinearBoardStates(tc.spec),
			}
			got, err := l.IssueStatus(context.Background(), "COD-7")
			if err != nil {
				t.Fatalf("IssueStatus error: %v", err)
			}
			if got != tc.want {
				t.Errorf("IssueStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

// The editor's choices come from the team's own workflow: every state is both a
// groupable row prefilled with its type-derived section and a pin a STATUS_* key
// may name.
func TestLinearStatusOptionsListsTheTeamWorkflow(t *testing.T) {
	cfg := Config{Team: "COD", APIKey: "lin_key", linearEndpoint: linearStatesFake(t).URL}
	opts, err := LinearStatusOptions(context.Background(), cfg)
	if err != nil {
		t.Fatalf("LinearStatusOptions error: %v", err)
	}
	wantColumns := []BoardColumnSuggestion{
		{Name: "Icebox", SuggestedGroup: StatusGroupBacklog},
		{Name: "In Progress", SuggestedGroup: StatusGroupStarted},
		{Name: "Ready for QA", SuggestedGroup: StatusGroupStarted},
		{Name: "Live", SuggestedGroup: StatusGroupDone},
		{Name: "Duplicate", SuggestedGroup: StatusGroupCanceled},
	}
	if len(opts.Columns) != len(wantColumns) {
		t.Fatalf("read %d groupable states, want %d", len(opts.Columns), len(wantColumns))
	}
	for i, want := range wantColumns {
		if opts.Columns[i] != want {
			t.Errorf("column %d = %+v, want %+v", i, opts.Columns[i], want)
		}
	}
	wantPins := []WorkflowOption{
		{Name: "Icebox", Category: "backlog"},
		{Name: "In Progress", Category: "started"},
		{Name: "Ready for QA", Category: "started"},
		{Name: "Live", Category: "completed"},
		{Name: "Duplicate", Category: "duplicate"},
	}
	for i, want := range wantPins {
		if opts.Pins[i] != want {
			t.Errorf("pin %d = %+v, want %+v", i, opts.Pins[i], want)
		}
	}
}

func TestLinearStatusOptionsNeedsATeam(t *testing.T) {
	if _, err := LinearStatusOptions(context.Background(), Config{APIKey: "lin_key"}); err == nil {
		t.Fatal("LinearStatusOptions accepted a repo with no team")
	}
}
