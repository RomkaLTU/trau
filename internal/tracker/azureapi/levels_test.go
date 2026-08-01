package azureapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// A Scrum project that has renamed nothing but stacks three portfolio backlogs:
// only the lowest of them is the Feature rung, and the requirement level is the
// Product Backlog Item its process happens to call it. The team files bugs as
// requirements, so a Bug sits beside the story rather than under it.
func TestBacklogLevelsPlacesTypesByRankNotByName(t *testing.T) {
	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/_apis/work/backlogconfiguration"):
			_, _ = w.Write([]byte(`{
				"portfolioBacklogs":[
					{"rank":3,"defaultWorkItemType":{"name":"Initiative"},"workItemTypes":[{"name":"Initiative"}]},
					{"rank":1,"defaultWorkItemType":{"name":"Capability"},"workItemTypes":[{"name":"Capability"}]},
					{"rank":2,"defaultWorkItemType":{"name":"Epic"},"workItemTypes":[{"name":"Epic"}]}],
				"requirementBacklog":{"defaultWorkItemType":{"name":"Product Backlog Item"},
					"workItemTypes":[{"name":"Product Backlog Item"},{"name":"Bug"}]},
				"taskBacklog":{"defaultWorkItemType":{"name":"Task"},"workItemTypes":[{"name":"Task"}]},
				"bugWorkItems":{"defaultWorkItemType":{"name":"Bug"},"workItemTypes":[{"name":"Bug"}]}}`))
		case strings.HasSuffix(r.URL.Path, "/_apis/work/teamsettings"):
			_, _ = w.Write([]byte(`{"bugsBehavior":"asRequirements"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer srv.Close()

	levels, err := New(srv.URL, "pat").BacklogLevels(context.Background(), "Contoso", "Contoso Team")
	if err != nil {
		t.Fatalf("BacklogLevels: %v", err)
	}

	for itemType, want := range map[string]Level{
		"Initiative":           LevelEpic,
		"Epic":                 LevelEpic,
		"Capability":           LevelFeature,
		"Product Backlog Item": LevelRequirement,
		"Bug":                  LevelRequirement,
		"Task":                 LevelTask,
		"Impediment":           "",
	} {
		if got := levels.Of(itemType); got != want {
			t.Errorf("Of(%q) = %q, want %q", itemType, got, want)
		}
	}
	if got := levels.Default(LevelRequirement); got != "Product Backlog Item" {
		t.Errorf("Default(requirement) = %q, want the backlog's own default type", got)
	}
	if want := []string{"Product Backlog Item", "Bug"}; !slices.Equal(levels.At(LevelRequirement), want) {
		t.Errorf("At(requirement) = %v, want %v", levels.At(LevelRequirement), want)
	}
	for _, path := range gotPaths {
		if !strings.HasPrefix(path, "/Contoso/Contoso Team/_apis/work/") {
			t.Errorf("path = %q, want it scoped to the named team", path)
		}
	}
}

// Where a Bug sits is the team's call, not the project's: the requirement backlog
// lists Bug whatever the team decided, so a team that files bugs as tasks moves it
// to the taskboard and the requirement level keeps only the story type. The two
// views of a level must agree — a type the picker offers is one the writer files.
func TestBacklogLevelsHonoursBugsAsTasks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_apis/work/teamsettings") {
			_, _ = w.Write([]byte(`{"bugsBehavior":"asTasks"}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"portfolioBacklogs":[{"rank":1,"defaultWorkItemType":{"name":"Feature"},"workItemTypes":[{"name":"Feature"}]}],
			"requirementBacklog":{"defaultWorkItemType":{"name":"User Story"},
				"workItemTypes":[{"name":"User Story"},{"name":"Bug"}]},
			"taskBacklog":{"defaultWorkItemType":{"name":"Task"},"workItemTypes":[{"name":"Task"}]},
			"bugWorkItems":{"defaultWorkItemType":{"name":"Bug"},"workItemTypes":[{"name":"Bug"}]}}`))
	}))
	defer srv.Close()

	levels, err := New(srv.URL, "pat").BacklogLevels(context.Background(), "Contoso", "")
	if err != nil {
		t.Fatalf("BacklogLevels: %v", err)
	}

	if got := levels.Of("Bug"); got != LevelTask {
		t.Errorf("Of(Bug) = %q, want task", got)
	}
	if want := []string{"User Story"}; !slices.Equal(levels.At(LevelRequirement), want) {
		t.Errorf("At(requirement) = %v, want %v", levels.At(LevelRequirement), want)
	}
	if want := []string{"Task", "Bug"}; !slices.Equal(levels.At(LevelTask), want) {
		t.Errorf("At(task) = %v, want %v", levels.At(LevelTask), want)
	}
}

// A team that files bugs on no backlog at all leaves a Bug with no level, and the
// project's requirement backlog listing Bug regardless must not be what decides —
// otherwise the item keeps a level-driven colour, and a pickability, its own team
// gave it nowhere. A behavior trau does not recognize reads the same way.
func TestBacklogLevelsDropsBugsTheTeamPlacesNowhere(t *testing.T) {
	for _, behavior := range []string{"off", "someFutureBehavior", ""} {
		t.Run(behavior, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/_apis/work/teamsettings") {
					_, _ = w.Write([]byte(`{"bugsBehavior":"` + behavior + `"}`))
					return
				}
				_, _ = w.Write([]byte(`{
					"portfolioBacklogs":[{"rank":1,"defaultWorkItemType":{"name":"Feature"},"workItemTypes":[{"name":"Feature"}]}],
					"requirementBacklog":{"defaultWorkItemType":{"name":"User Story"},
						"workItemTypes":[{"name":"User Story"},{"name":"Bug"}]},
					"taskBacklog":{"defaultWorkItemType":{"name":"Task"},"workItemTypes":[{"name":"Task"}]},
					"bugWorkItems":{"defaultWorkItemType":{"name":"Bug"},"workItemTypes":[{"name":"Bug"}]}}`))
			}))
			defer srv.Close()

			levels, err := New(srv.URL, "pat").BacklogLevels(context.Background(), "Contoso", "")
			if err != nil {
				t.Fatalf("BacklogLevels: %v", err)
			}

			if got := levels.Of("Bug"); got != "" {
				t.Errorf("Of(Bug) = %q, want no level", got)
			}
			if want := []string{"User Story"}; !slices.Equal(levels.At(LevelRequirement), want) {
				t.Errorf("At(requirement) = %v, want %v", levels.At(LevelRequirement), want)
			}
		})
	}
}

// A project that declares no requirement backlog places no type there, and that
// empty list still crosses the create-options JSON boundary: as null it takes the
// hierarchy picker — and the panel around it — down with it.
func TestBacklogLevelsListsAnEmptyLevelAsNoTypes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_apis/work/teamsettings") {
			_, _ = w.Write([]byte(`{"bugsBehavior":"off"}`))
			return
		}
		_, _ = w.Write([]byte(`{"taskBacklog":{"defaultWorkItemType":{"name":"Task"},"workItemTypes":[{"name":"Task"}]}}`))
	}))
	defer srv.Close()

	levels, err := New(srv.URL, "pat").BacklogLevels(context.Background(), "Contoso", "")
	if err != nil {
		t.Fatalf("BacklogLevels: %v", err)
	}

	types, err := json.Marshal(levels.At(LevelRequirement))
	if err != nil {
		t.Fatalf("marshal At(requirement): %v", err)
	}
	if string(types) != "[]" {
		t.Errorf("At(requirement) marshals to %s, want []", types)
	}
}
