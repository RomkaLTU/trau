package azureapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// boardsServer answers the boards and columns routes from a fixture keyed by the
// team segment the request carried, recording every path it served.
func boardsServer(t *testing.T, byTeam map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		body, ok := byTeam[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

func TestBoardColumnsReadsStateMappingsAndColumnType(t *testing.T) {
	srv, _ := boardsServer(t, map[string]string{
		"/Contoso/Platform/_apis/work/boards/b1/columns": `{"value":[
			{"name":"New","columnType":"incoming","stateMappings":{"User Story":"New","Bug":"New"}},
			{"name":"Ready to Develop","columnType":"inProgress","stateMappings":{"User Story":"New","Bug":"New"}},
			{"name":"Active","columnType":"inProgress","stateMappings":{"User Story":"Active","Bug":"Active"}},
			{"name":"Done","columnType":"outgoing","stateMappings":{"User Story":"Closed","Bug":"Closed"}}]}`,
	})

	columns, err := New(srv.URL, "pat").BoardColumns(context.Background(), "Contoso", "Platform", "b1")
	if err != nil {
		t.Fatalf("BoardColumns: %v", err)
	}
	want := []string{"New", "Ready to Develop", "Active", "Done"}
	got := make([]string, len(columns))
	for i, col := range columns {
		got[i] = col.Name
	}
	if !slices.Equal(got, want) {
		t.Errorf("columns = %v, want them in board order %v", got, want)
	}
	if columns[0].ColumnType != "incoming" {
		t.Errorf("first columnType = %q, want incoming", columns[0].ColumnType)
	}
	if !slices.Equal(columns[0].States, []string{"New"}) {
		t.Errorf("states of New = %v, want the one distinct state both types map to", columns[0].States)
	}
}

// Two boards of one team share every column but Ready to Develop, and the two
// teams share the Stories board's columns outright. The union has to keep one row
// per column in board order, gaining the states the later board maps onto it.
func TestTeamBoardColumnsUnionsBoardsAndTeams(t *testing.T) {
	srv, paths := boardsServer(t, map[string]string{
		"/Contoso/Platform/_apis/work/boards": `{"value":[
			{"id":"stories","name":"Stories"},{"id":"features","name":"Features"}]}`,
		"/Contoso/Platform/_apis/work/boards/stories/columns": `{"value":[
			{"name":"New","columnType":"incoming","stateMappings":{"User Story":"New"}},
			{"name":"Ready to Develop","columnType":"inProgress","stateMappings":{"User Story":"New"}},
			{"name":"Done","columnType":"outgoing","stateMappings":{"User Story":"Closed"}}]}`,
		"/Contoso/Platform/_apis/work/boards/features/columns": `{"value":[
			{"name":"New","columnType":"incoming","stateMappings":{"Feature":"New"}},
			{"name":"Done","columnType":"outgoing","stateMappings":{"Feature":"Removed"}}]}`,
		"/Contoso/Payments/_apis/work/boards": `{"value":[{"id":"stories","name":"Stories"}]}`,
		"/Contoso/Payments/_apis/work/boards/stories/columns": `{"value":[
			{"name":"new","columnType":"incoming","stateMappings":{"Bug":"New"}},
			{"name":"Blocked","columnType":"inProgress","stateMappings":{"Bug":"Active"}}]}`,
	})

	columns, err := New(srv.URL, "pat").
		TeamBoardColumns(context.Background(), "Contoso", []string{"Platform", " ", "Payments"})
	if err != nil {
		t.Fatalf("TeamBoardColumns: %v", err)
	}
	want := []string{"New", "Ready to Develop", "Done", "Blocked"}
	got := make([]string, len(columns))
	for i, col := range columns {
		got[i] = col.Name
	}
	if !slices.Equal(got, want) {
		t.Fatalf("columns = %v, want %v — deduped by name, first position kept", got, want)
	}
	if !slices.Equal(columns[2].States, []string{"Closed", "Removed"}) {
		t.Errorf("states of Done = %v, want both boards' mappings merged", columns[2].States)
	}
	for _, path := range *paths {
		if !strings.HasPrefix(path, "/Contoso/Platform/") && !strings.HasPrefix(path, "/Contoso/Payments/") {
			t.Errorf("path = %q, want it scoped to a named team", path)
		}
	}
}

// With no team configured the team segment is left out entirely, which is how the
// route resolves the project's default team.
func TestTeamBoardColumnsAsksTheDefaultTeamWhenNoneIsNamed(t *testing.T) {
	srv, paths := boardsServer(t, map[string]string{
		"/Contoso/_apis/work/boards": `{"value":[{"id":"b1","name":"Stories"}]}`,
		"/Contoso/_apis/work/boards/b1/columns": `{"value":[
			{"name":"New","columnType":"incoming","stateMappings":{"User Story":"New"}}]}`,
	})

	columns, err := New(srv.URL, "pat").TeamBoardColumns(context.Background(), "Contoso", nil)
	if err != nil {
		t.Fatalf("TeamBoardColumns: %v", err)
	}
	if len(columns) != 1 || columns[0].Name != "New" {
		t.Fatalf("columns = %v, want the default team's single column", columns)
	}
	for _, path := range *paths {
		if !strings.HasPrefix(path, "/Contoso/_apis/work/") {
			t.Errorf("path = %q, want no team segment", path)
		}
	}
}

// A rejected PAT has to reach the caller as ErrUnauthorized through the wrapping,
// or the hub cannot tell a lapsed token from a project with no boards.
func TestTeamBoardColumnsSurfacesAuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "pat").TeamBoardColumns(context.Background(), "Contoso", []string{"Platform"})
	if err == nil {
		t.Fatal("TeamBoardColumns succeeded against a 401")
	}
	if msg := AuthErrorMessageFor(err, srv.URL); msg == "" {
		t.Errorf("err = %v, want it to unwrap to ErrUnauthorized", err)
	}
	if !strings.Contains(err.Error(), "Platform") {
		t.Errorf("err = %v, want it to name the team it asked", err)
	}
}

func TestBoardsRequireCredentials(t *testing.T) {
	c := New("", "")
	if _, err := c.Boards(context.Background(), "Contoso", ""); err != ErrNotEnabled {
		t.Errorf("Boards err = %v, want ErrNotEnabled", err)
	}
	if _, err := c.BoardColumns(context.Background(), "Contoso", "", "b1"); err != ErrNotEnabled {
		t.Errorf("BoardColumns err = %v, want ErrNotEnabled", err)
	}
	if _, err := c.TeamBoardColumns(context.Background(), "Contoso", nil); err != ErrNotEnabled {
		t.Errorf("TeamBoardColumns err = %v, want ErrNotEnabled", err)
	}
}
