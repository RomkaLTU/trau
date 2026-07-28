package webserver

import (
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// The reasons a start-repo suggestion carries, so the selector can say why it
// pre-selected what it did.
const (
	startRepoRemembered = "remembered"
	startRepoNamed      = "named"
	startRepoRecent     = "recent"
	startRepoFirst      = "first"
)

// ProjectMemberView is one member repo a run can be started in: the identifier
// the repo routes address it by, and its root.
type ProjectMemberView struct {
	Name string `json:"name"`
	Root string `json:"root"`
}

// ProjectStartResponse is the /api/v1/projects/{project}/start-repo resource: the
// member repos a ticket can be started in, in project order, and which one the
// hub suggests. Suggested names a repo in Repos; Reason says what earned it.
type ProjectStartResponse struct {
	Project   string              `json:"project"`
	Ticket    string              `json:"ticket,omitempty"`
	Repos     []ProjectMemberView `json:"repos"`
	Suggested string              `json:"suggested"`
	Reason    string              `json:"reason"`
}

// handleProjectStartRepo answers which member repo a ticket should start in. It
// is only the pre-selection the picker opens on, so the answer stays cheap and
// deterministic and never calls a model.
func (s *Server) handleProjectStartRepo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	proj, err := s.stores.Projects().Get(r.PathValue("project"))
	if err != nil {
		writeProjectError(w, err, "failed to read project")
		return
	}
	members := s.projectMembers(proj)
	if len(members) == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("project %q has no member repos to start in", proj.ID),
		})
		return
	}
	ticket := strings.TrimSpace(r.URL.Query().Get("id"))
	members, err = s.startMembers(members, ticket)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read issue: " + err.Error()})
		return
	}
	suggested, reason, err := s.suggestStartRepo(proj, members, ticket)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suggest start repo: " + err.Error()})
		return
	}
	views := make([]ProjectMemberView, 0, len(members))
	for _, repo := range members {
		views = append(views, ProjectMemberView{Name: repo.Name, Root: repo.Root})
	}
	writeJSON(w, http.StatusOK, ProjectStartResponse{
		Project:   proj.ID,
		Ticket:    ticket,
		Repos:     views,
		Suggested: suggested.Name,
		Reason:    reason,
	})
}

// rememberStartRepo records the member repo a project's ticket was started in, so
// re-queueing or resuming it opens the picker on the same repo and the project's
// next unmatched ticket falls back to it. Best-effort: the work is already
// queued, and failing to remember must not turn that into an error.
func (s *Server) rememberStartRepo(root, ticket string) {
	projects := s.stores.Projects()
	project, err := projects.Holder(root)
	if err == nil && project != "" {
		err = projects.RecordTicketRepo(project, ticket, root)
	}
	if err != nil {
		logger.Verbosef("remember start repo %s for %s: %v", root, ticket, err)
	}
}

// projectMembers resolves the project's member roots to the repos the rest of the
// API addresses, keeping the project's own order. A root the hub cannot resolve
// is dropped rather than served as a half-known repo the queue could not accept.
func (s *Server) projectMembers(proj hubstore.Project) []registry.Repo {
	members := make([]registry.Repo, 0, len(proj.Repos))
	for _, root := range proj.Repos {
		if repo, ok := s.findRepo(root); ok {
			members = append(members, repo)
		}
	}
	return members
}

// startMembers keeps the members whose queue could take the ticket. An internal
// ticket is authoritative in one member's issue store and unknown to the rest of
// the project, so it can only be started where it lives; a tracker ticket is
// shared by every member and leaves the list whole.
func (s *Server) startMembers(members []registry.Repo, ticket string) ([]registry.Repo, error) {
	if ticket == "" {
		return members, nil
	}
	for _, repo := range members {
		_, internal, err := s.stores.Issues().Internal(repo.Root, ticket)
		if err != nil {
			return nil, err
		}
		if internal {
			return []registry.Repo{repo}, nil
		}
	}
	return members, nil
}

// suggestStartRepo ranks the ticket's own history above the name heuristic: a
// ticket that already ran somewhere goes back there.
func (s *Server) suggestStartRepo(proj hubstore.Project, members []registry.Repo, ticket string) (registry.Repo, string, error) {
	projects := s.stores.Projects()
	if ticket != "" {
		remembered, err := projects.TicketRepo(proj.ID, ticket)
		if err != nil {
			return registry.Repo{}, "", err
		}
		if repo, ok := memberByRoot(members, remembered); ok {
			return repo, startRepoRemembered, nil
		}
		text, err := s.ticketText(members, ticket)
		if err != nil {
			return registry.Repo{}, "", err
		}
		if repo, ok := namedMember(members, text); ok {
			return repo, startRepoNamed, nil
		}
	}
	recent, err := projects.LastTicketRepo(proj.ID)
	if err != nil {
		return registry.Repo{}, "", err
	}
	if repo, ok := memberByRoot(members, recent); ok {
		return repo, startRepoRecent, nil
	}
	return members[0], startRepoFirst, nil
}

// ticketText reads the ticket from the first member that has it synced, as the
// one string the hint matches member names against. Members of a project share
// one tracker, so whichever answers carries the same text.
func (s *Server) ticketText(members []registry.Repo, ticket string) (string, error) {
	for _, repo := range members {
		iss, found, err := s.stores.Issues().Get(repo.Root, ticket)
		if err != nil {
			return "", err
		}
		if found {
			return iss.Title + " " + iss.Description, nil
		}
	}
	return "", nil
}

// namedMember returns the member the text names, matching its repo name and its
// root's basename as whole words so a short name cannot match inside a longer
// one. The most specific name wins when several match.
func namedMember(members []registry.Repo, text string) (registry.Repo, bool) {
	haystack := words(text)
	if len(haystack) == 0 {
		return registry.Repo{}, false
	}
	var best registry.Repo
	bestLen := 0
	for _, repo := range members {
		for _, candidate := range []string{repo.Name, filepath.Base(repo.Root)} {
			name := words(candidate)
			if len(name) > bestLen && containsRun(haystack, name) {
				best, bestLen = repo, len(name)
			}
		}
	}
	return best, bestLen > 0
}

func memberByRoot(members []registry.Repo, root string) (registry.Repo, bool) {
	if root == "" {
		return registry.Repo{}, false
	}
	for _, repo := range members {
		if repo.Root == root {
			return repo, true
		}
	}
	return registry.Repo{}, false
}

// words lowercases s and splits it on everything that is not a letter or digit,
// so `image-service`, `image_service` and `image service` all read the same.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func containsRun(haystack, needle []string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if slices.Equal(haystack[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}
