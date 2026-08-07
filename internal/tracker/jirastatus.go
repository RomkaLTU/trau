package tracker

import (
	"context"
	"errors"
	"strings"

	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
)

// JiraStatusOptions reads the choices a Jira repo's status mapping is built from:
// every workflow status the project's issue types can reach, each with the
// section its statusCategory derives, and the same statuses as the pins a STATUS_*
// key may name. Both lists are one vocabulary here, as on Linear — a Jira board's
// columns are built out of the project's own statuses, so grouping and pinning
// speak the same names.
//
// The suggestion is deliberately static: it reads the category alone, with no
// resolution nuance. A resolution is a property of one issue, not of the status,
// so there is no won't-do answer to give a row that stands for every issue in it.
//
// Nothing is written and nothing is cached, so an editor asking twice sees
// whatever the project's workflows say now.
func JiraStatusOptions(ctx context.Context, cfg Config) (StatusOptions, error) {
	project := strings.TrimSpace(cfg.Team)
	if project == "" {
		return StatusOptions{}, errors.New("jira: no project key is configured for this repo")
	}
	statuses, err := jiraapi.New(cfg.BaseURL, cfg.Email, cfg.APIKey).ProjectStatuses(ctx, project)
	if err != nil {
		return StatusOptions{}, err
	}
	out := StatusOptions{
		Columns: make([]BoardColumnSuggestion, 0, len(statuses)),
		Pins:    make([]WorkflowOption, 0, len(statuses)),
	}
	for _, st := range statuses {
		out.Columns = append(out.Columns, BoardColumnSuggestion{
			Name:           st.Name,
			SuggestedGroup: mapJiraGroup(st.Category, ""),
		})
		out.Pins = append(out.Pins, WorkflowOption{Name: st.Name, Category: st.Category})
	}
	return out, nil
}
