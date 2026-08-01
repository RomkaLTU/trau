package azureapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// defaultTeamField is the field a team's board is keyed on under every stock
// process. A customized project can point its boards at another field, which the
// teamfieldvalues route reports, so the constant is only the fallback for a
// response that names none.
const defaultTeamField = "System.AreaPath"

// BoardScope is the slice of a team project a repo mirrors and picks from: an Area
// Path and the areas of the teams the repo names. Both narrow, so a query carrying
// each asks for their intersection; a zero BoardScope is the whole team project.
type BoardScope struct {
	AreaPath string
	Teams    []TeamAreas
}

// TeamAreas is one Azure DevOps team's board scope: the field its boards are keyed
// on and the values it owns.
type TeamAreas struct {
	Field  string
	Values []TeamAreaValue
}

// TeamAreaValue is one value a team's board covers, either on its own or together
// with everything nested beneath it.
type TeamAreaValue struct {
	Value           string
	IncludeChildren bool
}

// ResolveScope resolves the scope a board covers: areaPath as given, plus the areas
// every named team owns. The @TeamAreas macro exists only in the web portal and
// answers empty over REST, so a team's areas are read explicitly here and inlined
// into the query. A named team that declares no area would silently widen the query
// back to the whole team project, so it is refused instead.
func (c *Client) ResolveScope(ctx context.Context, project, areaPath string, teams []string) (BoardScope, error) {
	scope := BoardScope{AreaPath: strings.TrimSpace(areaPath)}
	for _, name := range teams {
		areas, err := c.TeamFieldValues(ctx, project, name)
		if err != nil {
			return BoardScope{}, fmt.Errorf("read areas of team %q: %w", name, err)
		}
		if len(areas.Values) == 0 {
			return BoardScope{}, fmt.Errorf("azure: team %q declares no area path to scope the board to", name)
		}
		scope.Teams = append(scope.Teams, areas)
	}
	return scope, nil
}

// Covers reports whether the board scope covers one work item. The board's own
// query is the only exact answer: a team keys its board on whatever field its
// settings name, which a work item's payload need not carry, and an includeChildren
// value is a hierarchy test WIQL evaluates server-side.
func (c *Client) Covers(ctx context.Context, project string, scope BoardScope, id int) (bool, error) {
	if !c.enabled() {
		return false, ErrNotEnabled
	}
	ids, err := c.query(ctx, project, coversWIQL(project, scope, id), 1)
	if err != nil {
		return false, err
	}
	return len(ids) > 0, nil
}

// coversWIQL asks the board about a single work item, narrowed by the same scope
// clause every other board query carries.
func coversWIQL(project string, scope BoardScope, id int) string {
	return "SELECT [System.Id] FROM WorkItems" +
		" WHERE [System.TeamProject] = " + wiqlString(project) +
		scopeClause(scope) +
		" AND [System.Id] = " + strconv.Itoa(id)
}

// TeamFieldValues reads the board scope one team declares. The route accepts a team
// name as well as its id, so a configured name resolves without a GUID lookup
// first.
func (c *Client) TeamFieldValues(ctx context.Context, project, team string) (TeamAreas, error) {
	if !c.enabled() {
		return TeamAreas{}, ErrNotEnabled
	}
	var dst struct {
		Field struct {
			ReferenceName string `json:"referenceName"`
		} `json:"field"`
		Values []struct {
			Value           string `json:"value"`
			IncludeChildren bool   `json:"includeChildren"`
		} `json:"values"`
	}
	path := workPath(project, team, "/teamsettings/teamfieldvalues")
	if err := c.do(ctx, http.MethodGet, path, nil, &dst); err != nil {
		return TeamAreas{}, err
	}
	out := TeamAreas{Field: dst.Field.ReferenceName}
	for _, v := range dst.Values {
		if strings.TrimSpace(v.Value) == "" {
			continue
		}
		out.Values = append(out.Values, TeamAreaValue{Value: v.Value, IncludeChildren: v.IncludeChildren})
	}
	return out, nil
}

// scopeClause renders the WHERE tail that narrows a query to the board's scope.
func scopeClause(scope BoardScope) string {
	return areaClause(scope.AreaPath) + teamsClause(scope.Teams)
}

// areaClause narrows a query to an Area Path, or to nothing at all when none is
// configured. The test is UNDER rather than an equality so a parent area carries
// its children, the way Azure DevOps itself scopes a board to an area.
func areaClause(areaPath string) string {
	if path := strings.TrimSpace(areaPath); path != "" {
		return " AND [System.AreaPath] UNDER " + wiqlString(path)
	}
	return ""
}

// teamsClause narrows a query to the union of the areas the named teams own. A team
// owns several values and a repo can name several teams, so every value is one OR
// term of a single clause — a repo scoped to two teams mirrors both boards, not
// their overlap.
func teamsClause(teams []TeamAreas) string {
	var terms []string
	for _, team := range teams {
		field := strings.TrimSpace(team.Field)
		if field == "" {
			field = defaultTeamField
		}
		for _, v := range team.Values {
			op := " = "
			if v.IncludeChildren {
				op = " UNDER "
			}
			terms = append(terms, "["+field+"]"+op+wiqlString(v.Value))
		}
	}
	if len(terms) == 0 {
		return ""
	}
	return " AND (" + strings.Join(terms, " OR ") + ")"
}
