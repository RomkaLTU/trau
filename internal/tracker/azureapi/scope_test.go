package azureapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A named team scopes the pull to the areas its own board declares, read from
// teamfieldvalues because @TeamAreas answers empty over REST. The query also has to
// ask for time precision: without it the endpoint runs at day precision and refuses
// the cursor's timestamp outright.
func TestSyncIDsScopesToTheTeamsAreasAtTimePrecision(t *testing.T) {
	var gotWIQL, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_apis/work/teamsettings/teamfieldvalues"):
			if want := "/Contoso/Contoso%20Platform/_apis/work/teamsettings/teamfieldvalues"; r.URL.EscapedPath() != want {
				t.Errorf("path = %q, want %q", r.URL.EscapedPath(), want)
			}
			_, _ = w.Write([]byte(`{"field":{"referenceName":"System.AreaPath"},"values":[
				{"value":"Contoso\\Platform","includeChildren":true},
				{"value":"Contoso\\Shared","includeChildren":false}]}`))
		case strings.HasSuffix(r.URL.Path, "/wiql"):
			body, _ := io.ReadAll(r.Body)
			var req struct{ Query string }
			_ = json.Unmarshal(body, &req)
			gotWIQL = req.Query
			gotQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"workItems":[{"id":6694}]}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "pat")
	ctx := context.Background()

	scope, err := c.ResolveScope(ctx, "Contoso", "", []string{"Contoso Platform"})
	if err != nil {
		t.Fatalf("ResolveScope: %v", err)
	}
	ids, err := c.SyncIDs(ctx, "Contoso", scope, "2026-07-31T12:03:41Z")
	if err != nil {
		t.Fatalf("SyncIDs: %v", err)
	}

	if len(ids) != 1 || ids[0] != 6694 {
		t.Fatalf("ids = %v, want [6694]", ids)
	}
	if !strings.Contains(gotQuery, "timePrecision=true") {
		t.Errorf("query = %q, want timePrecision=true", gotQuery)
	}
	want := ` AND ([System.AreaPath] UNDER 'Contoso\Platform' OR [System.AreaPath] = 'Contoso\Shared')`
	if !strings.Contains(gotWIQL, want) {
		t.Errorf("WIQL = %q, want it to carry %q", gotWIQL, want)
	}
	if !strings.Contains(gotWIQL, `[System.ChangedDate] >= '2026-07-31T12:03:41Z'`) {
		t.Errorf("WIQL = %q, want the cursor's timestamp", gotWIQL)
	}
}

// A team that declares no area would widen the query back to the whole team
// project, which is the opposite of what naming it asked for.
func TestResolveScopeRefusesATeamWithNoArea(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"field":{"referenceName":"System.AreaPath"},"values":[]}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "pat").ResolveScope(context.Background(), "Contoso", "", []string{"Ghost"})
	if err == nil {
		t.Fatal("ResolveScope accepted a team with no area")
	}
	if !strings.Contains(err.Error(), `"Ghost"`) {
		t.Errorf("err = %q, want it to name the team", err)
	}
}
