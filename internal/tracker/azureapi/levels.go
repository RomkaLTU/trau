package azureapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Level is the rung a work-item type occupies in the Epic → Feature → User
// Story/Bug → Task hierarchy, normalized away from the names any one process
// happens to use. Azure processes are customizable — Agile calls the requirement
// level "User Story", Scrum "Product Backlog Item", CMMI "Requirement", and a
// project may rename its types or add portfolio backlogs of its own — so a level
// is read from the project's own backlog configuration, never matched against a
// literal type name.
type Level string

const (
	LevelEpic        Level = "epic"        // any portfolio backlog above the lowest one
	LevelFeature     Level = "feature"     // the lowest portfolio backlog, directly above requirement
	LevelRequirement Level = "requirement" // the requirement backlog
	LevelTask        Level = "task"        // the taskboard
)

// Levels places a team project's work-item types on the levels trau reasons
// about. A type the configuration places nowhere — one on a hidden backlog, or a
// Bug on a team that has bugs turned off — has no level at all.
type Levels struct {
	byType map[string]Level
	types  map[Level][]string
}

// Of returns the level itemType sits on, or "" when the configuration places it
// nowhere.
func (l Levels) Of(itemType string) Level { return l.byType[levelKey(itemType)] }

// At lists the work-item types the project places on level, each backlog's own
// default type first, so a create with nothing pinned files At(level)[0]. A level
// the project declares nothing on lists nothing rather than nil: the list crosses a
// JSON boundary, where nil would reach the picker as null.
func (l Levels) At(level Level) []string { return append([]string{}, l.types[level]...) }

// Default names the work-item type a create at level files, or "" when the
// project has no backlog there.
func (l Levels) Default(level Level) string {
	if types := l.types[level]; len(types) > 0 {
		return types[0]
	}
	return ""
}

// BacklogLevels reads how a team project stacks its work-item types. The read is
// team-scoped because the placement of a Bug is a team decision (bugsBehavior),
// not a project one: team names the board to ask, and "" asks the project's
// default team — what the route resolves when the segment is left out.
func (c *Client) BacklogLevels(ctx context.Context, project, team string) (Levels, error) {
	if !c.enabled() {
		return Levels{}, ErrNotEnabled
	}
	var config struct {
		PortfolioBacklogs  []backlogLevel `json:"portfolioBacklogs"`
		RequirementBacklog backlogLevel   `json:"requirementBacklog"`
		TaskBacklog        backlogLevel   `json:"taskBacklog"`
		BugWorkItems       backlogLevel   `json:"bugWorkItems"`
	}
	if err := c.do(ctx, http.MethodGet, workPath(project, team, "/backlogconfiguration"), nil, &config); err != nil {
		return Levels{}, fmt.Errorf("read backlog configuration: %w", err)
	}
	var settings struct {
		BugsBehavior string `json:"bugsBehavior"`
	}
	if err := c.do(ctx, http.MethodGet, workPath(project, team, "/teamsettings"), nil, &settings); err != nil {
		return Levels{}, fmt.Errorf("read team settings: %w", err)
	}

	levels := Levels{byType: map[string]Level{}, types: map[Level][]string{}}
	portfolio := slices.Clone(config.PortfolioBacklogs)
	slices.SortStableFunc(portfolio, func(a, b backlogLevel) int { return a.Rank - b.Rank })
	for i, backlog := range portfolio {
		// Rank climbs away from the requirement level, so the lowest-ranked portfolio
		// backlog is the one sitting directly above it — the Feature rung, whatever
		// the process calls it.
		level := LevelEpic
		if i == 0 {
			level = LevelFeature
		}
		levels.place(level, backlog)
	}
	levels.place(LevelRequirement, config.RequirementBacklog)
	levels.place(LevelTask, config.TaskBacklog)
	if level, placed := bugLevel(settings.BugsBehavior); placed {
		levels.place(level, config.BugWorkItems)
	} else {
		levels.unplace(config.BugWorkItems)
	}
	return levels, nil
}

// backlogLevel is one rung of the project's backlog configuration: the types it
// holds, the one a create defaults to, and its rank among the portfolio backlogs.
type backlogLevel struct {
	Rank                int        `json:"rank"`
	DefaultWorkItemType typeName   `json:"defaultWorkItemType"`
	WorkItemTypes       []typeName `json:"workItemTypes"`
}

type typeName struct {
	Name string `json:"name"`
}

// place records one backlog's work-item types on a level, its own default type
// first so a create with nothing pinned files what the board itself would. A type
// two backlogs both claim lands on the later one only — every project lists Bug on
// a requirement or task backlog *and* under bugWorkItems, and the team's
// bugsBehavior placement is the one that decides.
func (l *Levels) place(level Level, backlog backlogLevel) {
	for _, name := range backlog.names() {
		l.detach(name)
		l.byType[levelKey(name)] = level
		l.types[level] = append(l.types[level], name)
	}
}

// unplace takes a backlog's work-item types off every level, for a team whose
// settings place them nowhere. Skipping the placement is not enough: the project
// lists Bug on its requirement or task backlog whatever the team decided, so a Bug
// only keeps no level once it is taken back off the one that listing put it on.
func (l *Levels) unplace(backlog backlogLevel) {
	for _, name := range backlog.names() {
		l.detach(name)
	}
}

// detach drops name from whichever level it currently sits on.
func (l *Levels) detach(name string) {
	key := levelKey(name)
	prior, ok := l.byType[key]
	if !ok {
		return
	}
	l.types[prior] = slices.DeleteFunc(l.types[prior], func(placed string) bool {
		return levelKey(placed) == key
	})
	delete(l.byType, key)
}

// names lists the backlog's own work-item types, its default first.
func (b backlogLevel) names() []string {
	names := make([]string, 0, len(b.WorkItemTypes)+1)
	if preferred := strings.TrimSpace(b.DefaultWorkItemType.Name); preferred != "" {
		names = append(names, preferred)
	}
	for _, t := range b.WorkItemTypes {
		if name := strings.TrimSpace(t.Name); name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// bugLevel reads the team's bugsBehavior — the setting behind the reference
// diagram's note that a Bug sits at either level. A team that has bugs turned off,
// or names a behavior trau does not know, places them on neither.
func bugLevel(behavior string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(behavior)) {
	case "asrequirements":
		return LevelRequirement, true
	case "astasks":
		return LevelTask, true
	default:
		return "", false
	}
}

// workPath builds a team-scoped Work API route. An empty team leaves the segment
// out, which resolves the project's default team.
func workPath(project, team, suffix string) string {
	path := "/" + url.PathEscape(strings.TrimSpace(project))
	if team = strings.TrimSpace(team); team != "" {
		path += "/" + url.PathEscape(team)
	}
	return path + "/_apis/work" + suffix
}

func levelKey(itemType string) string { return strings.ToLower(strings.TrimSpace(itemType)) }
