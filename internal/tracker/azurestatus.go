package tracker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/RomkaLTU/trau/internal/tracker/azureapi"
)

// BoardColumnSuggestion is one Kanban column a repo's grouping may map, with the
// group the column's own state mappings suggest filing it under.
type BoardColumnSuggestion struct {
	Name           string
	SuggestedGroup StatusGroup
}

// StatusOptions is what a status-mapping editor picks from: the board columns a
// grouping maps, and the workflow statuses a STATUS_* pin may name. The two lists
// are deliberately different vocabularies — a pin writes a state, never a board
// column, because Azure DevOps refuses a System.BoardColumn write (ADR 0036).
type StatusOptions struct {
	Columns []BoardColumnSuggestion
	Pins    []WorkflowOption
}

// AzureStatusOptions reads the choices an Azure DevOps repo's status mapping is
// built from: every Kanban column the repo's teams carry, each with a suggested
// group, and the states the work-item type trau files can hold. Nothing is
// written and nothing is cached beyond the client's own per-call state cache, so
// an editor asking twice sees whatever the board says now.
func AzureStatusOptions(ctx context.Context, cfg Config) (StatusOptions, error) {
	project := strings.TrimSpace(cfg.Team)
	if project == "" {
		return StatusOptions{}, errors.New("azure: no team project is configured for this repo")
	}
	client := azureapi.New(cfg.BaseURL, cfg.APIKey)
	levels, err := client.BacklogLevels(ctx, project, levelTeam(cfg.BoardTeams))
	if err != nil {
		return StatusOptions{}, err
	}
	itemType := levels.Default(azureapi.LevelRequirement)
	if itemType == "" {
		return StatusOptions{}, fmt.Errorf("azure: %s declares no requirement-level work-item type", project)
	}
	states, err := client.StateCategories(ctx, project, itemType)
	if err != nil {
		return StatusOptions{}, err
	}
	columns, err := client.TeamBoardColumns(ctx, project, cfg.BoardTeams)
	if err != nil {
		return StatusOptions{}, err
	}

	out := StatusOptions{
		Columns: make([]BoardColumnSuggestion, 0, len(columns)),
		Pins:    make([]WorkflowOption, 0, len(states)),
	}
	pin := cfg.StatusOverrides[StageTodo]
	for _, col := range columns {
		out.Columns = append(out.Columns, BoardColumnSuggestion{
			Name:           col.Name,
			SuggestedGroup: suggestBoardGroup(states, pin, col),
		})
	}
	for _, s := range states {
		out.Pins = append(out.Pins, WorkflowOption{Name: s.Name, Category: s.Category})
	}
	return out, nil
}

// suggestBoardGroup derives the group a board column would file its work under if
// nobody had mapped it — what an editor prefills a fresh mapping with. It reads
// the column's own state mappings through the same category path the unmapped
// grouping takes (ADR 0033), so a suggestion never contradicts what the board
// already shows. A column whose types disagree takes the least advanced of their
// groups, and one mapping no state trau recognizes falls back to the rung Azure
// DevOps files the column on itself.
func suggestBoardGroup(states []azureapi.State, pin string, column azureapi.BoardColumn) StatusGroup {
	seen := map[StatusGroup]bool{}
	for _, state := range column.States {
		if group := mapAzureGroup(states, pin, state, ""); group != StatusGroupUnknown {
			seen[group] = true
		}
	}
	for _, group := range mappableGroups {
		if seen[group] {
			return group
		}
	}
	return columnTypeGroup(column.ColumnType)
}

// columnTypeGroup maps the rung Azure DevOps files a column on onto a status
// group, for a column whose state mappings carry nothing recognizable.
func columnTypeGroup(columnType string) StatusGroup {
	switch strings.ToLower(strings.TrimSpace(columnType)) {
	case "incoming":
		return StatusGroupUnstarted
	case "inprogress":
		return StatusGroupStarted
	case "outgoing":
		return StatusGroupDone
	default:
		return StatusGroupUnknown
	}
}
