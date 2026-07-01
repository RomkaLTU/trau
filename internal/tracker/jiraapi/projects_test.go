package jiraapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ListProjects follows startAt pagination across pages, accumulating and mapping
// every project's key/name/id in order.
func TestListProjectsPaginates(t *testing.T) {
	var starts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		starts = append(starts, q.Get("startAt"))
		if q.Get("maxResults") == "" {
			t.Errorf("missing maxResults query param")
		}
		w.Header().Set("Content-Type", "application/json")
		switch q.Get("startAt") {
		case "0":
			_, _ = w.Write([]byte(`{"values":[{"key":"ENG","name":"Engineering","id":"1"},{"key":"OPS","name":"Operations","id":"2"}],"startAt":0,"maxResults":2,"total":3,"isLast":false}`))
		default:
			_, _ = w.Write([]byte(`{"values":[{"key":"MKT","name":"Marketing","id":"3"}],"startAt":2,"maxResults":2,"total":3,"isLast":true}`))
		}
	}))
	defer srv.Close()

	projects, err := New(srv.URL, "me@acme.com", "tok").ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects error: %v", err)
	}
	if len(starts) != 2 || starts[0] != "0" || starts[1] != "2" {
		t.Errorf("paged with startAt %v, want [0 2]", starts)
	}
	want := []Project{
		{Key: "ENG", Name: "Engineering", ID: "1"},
		{Key: "OPS", Name: "Operations", ID: "2"},
		{Key: "MKT", Name: "Marketing", ID: "3"},
	}
	if len(projects) != len(want) {
		t.Fatalf("projects = %+v, want %+v", projects, want)
	}
	for i, p := range want {
		if projects[i] != p {
			t.Errorf("project[%d] = %+v, want %+v", i, projects[i], p)
		}
	}
}

func TestListProjectsDisabledWithoutToken(t *testing.T) {
	if _, err := New("", "", "").ListProjects(context.Background()); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("ListProjects err = %v, want ErrNotEnabled", err)
	}
}
