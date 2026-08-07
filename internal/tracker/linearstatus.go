package tracker

import (
	"context"
	"errors"
	"strings"

	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

// LinearStatusOptions reads the choices a Linear repo's status mapping is built
// from: the team's own workflow states, each with the section its Linear state
// type derives, and the same states as the pins a STATUS_* key may name. Both
// lists are one vocabulary here — unlike Azure DevOps, a Linear board's columns
// *are* its workflow states, so grouping and pinning speak the same names.
//
// Nothing is written and nothing is cached, so an editor asking twice sees
// whatever the team's workflow says now.
func LinearStatusOptions(ctx context.Context, cfg Config) (StatusOptions, error) {
	team := strings.TrimSpace(cfg.Team)
	if team == "" {
		return StatusOptions{}, errors.New("linear: no team is configured for this repo")
	}
	client := linearapi.New(cfg.APIKey)
	if cfg.linearEndpoint != "" {
		client.Endpoint = cfg.linearEndpoint
	}
	states, err := client.TeamWorkflowStates(ctx, team)
	if err != nil {
		return StatusOptions{}, err
	}
	out := StatusOptions{
		Columns: make([]BoardColumnSuggestion, 0, len(states)),
		Pins:    make([]WorkflowOption, 0, len(states)),
	}
	for _, s := range states {
		out.Columns = append(out.Columns, BoardColumnSuggestion{
			Name:           s.Name,
			SuggestedGroup: mapLinearGroup(s.Type),
		})
		out.Pins = append(out.Pins, WorkflowOption{Name: s.Name, Category: s.Type})
	}
	return out, nil
}
