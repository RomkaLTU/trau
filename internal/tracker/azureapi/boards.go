package azureapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Board is one Kanban board a team keeps. A team has one board per backlog level
// it shows — Stories and Features under the stock processes — and each carries
// its own column set.
type Board struct {
	ID   string
	Name string
}

// BoardColumn is one column on a team's Kanban board: the unit a team reads its
// board by, and the key AZURE_BOARD_STATES maps onto a status group (ADR 0036).
type BoardColumn struct {
	Name string
	// ColumnType is the rung Azure DevOps files the column on itself — incoming,
	// inProgress or outgoing — which is all a column whose state mappings say
	// nothing recognizable has left to classify it by.
	ColumnType string
	// States lists the work-item states the column maps to, one per work-item type
	// the board carries. Azure DevOps delivers them as a JSON object, so they are
	// deduplicated and sorted here: a suggestion derived from them must not depend
	// on Go's map iteration order.
	States []string
}

// Boards lists the Kanban boards a team keeps. An empty team leaves the segment
// out of the route, which resolves the project's default team — the same
// convention the backlog-configuration read follows.
func (c *Client) Boards(ctx context.Context, project, team string) ([]Board, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	var dst struct {
		Value []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"value"`
	}
	if err := c.do(ctx, http.MethodGet, workPath(project, team, "/boards"), nil, &dst); err != nil {
		return nil, err
	}
	out := make([]Board, 0, len(dst.Value))
	for _, b := range dst.Value {
		if strings.TrimSpace(b.ID) == "" {
			continue
		}
		out = append(out, Board{ID: b.ID, Name: b.Name})
	}
	return out, nil
}

// BoardColumns lists one board's columns in board order, left to right.
func (c *Client) BoardColumns(ctx context.Context, project, team, board string) ([]BoardColumn, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	var dst struct {
		Value []struct {
			Name          string            `json:"name"`
			ColumnType    string            `json:"columnType"`
			StateMappings map[string]string `json:"stateMappings"`
		} `json:"value"`
	}
	path := workPath(project, team, "/boards/"+url.PathEscape(strings.TrimSpace(board))+"/columns")
	if err := c.do(ctx, http.MethodGet, path, nil, &dst); err != nil {
		return nil, err
	}
	out := make([]BoardColumn, 0, len(dst.Value))
	for _, col := range dst.Value {
		if strings.TrimSpace(col.Name) == "" {
			continue
		}
		out = append(out, BoardColumn{
			Name:       strings.TrimSpace(col.Name),
			ColumnType: col.ColumnType,
			States:     mappedStates(col.StateMappings),
		})
	}
	return out, nil
}

// TeamBoardColumns lists every Kanban column the named teams' boards carry, in
// the order the boards present them and deduplicated by name: a project's Stories
// and Features boards share most of their columns, and two teams working one area
// routinely share all of them. A column met twice keeps its first position and
// gains the states the later board maps onto it, so the union classifies as well
// as either board alone. An empty team list asks the project's default team.
func (c *Client) TeamBoardColumns(ctx context.Context, project string, teams []string) ([]BoardColumn, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	var out []BoardColumn
	at := map[string]int{}
	for _, team := range boardTeams(teams) {
		boards, err := c.Boards(ctx, project, team)
		if err != nil {
			return nil, fmt.Errorf("list boards of %s: %w", teamLabel(project, team), err)
		}
		for _, board := range boards {
			columns, err := c.BoardColumns(ctx, project, team, board.ID)
			if err != nil {
				return nil, fmt.Errorf("list columns of board %q on %s: %w", board.Name, teamLabel(project, team), err)
			}
			for _, col := range columns {
				key := strings.ToLower(col.Name)
				if i, seen := at[key]; seen {
					out[i].States = union(out[i].States, col.States)
					continue
				}
				at[key] = len(out)
				out = append(out, col)
			}
		}
	}
	return out, nil
}

// boardTeams normalizes the configured team list into the teams to ask. An empty
// list becomes the single unnamed team, which the route resolves to the project's
// default one.
func boardTeams(teams []string) []string {
	out := make([]string, 0, len(teams))
	for _, team := range teams {
		if team = strings.TrimSpace(team); team != "" {
			out = append(out, team)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// teamLabel names the board a failed read addressed, for a message an operator
// can act on: the team when the repo names one, the project's default otherwise.
func teamLabel(project, team string) string {
	if team == "" {
		return "the default team of " + project
	}
	return "team " + team
}

// mappedStates flattens a column's work-item-type → state mappings into the
// distinct states the column stands for, sorted so the result is stable.
func mappedStates(mappings map[string]string) []string {
	out := make([]string, 0, len(mappings))
	for _, state := range mappings {
		if state = strings.TrimSpace(state); state != "" && !slices.Contains(out, state) {
			out = append(out, state)
		}
	}
	slices.Sort(out)
	return out
}

// union merges two state lists, keeping them sorted and distinct.
func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, state := range b {
		if !slices.Contains(out, state) {
			out = append(out, state)
		}
	}
	slices.Sort(out)
	return out
}
