package webserver

import (
	"net/http"

	"github.com/RomkaLTU/trau/internal/agent"
)

// WorkspaceView is one detected workspace as the app URL editors offer it: the
// three names an entry may carry to route to this workspace at run time.
type WorkspaceView struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	DirName string `json:"dir_name"`
}

type WorkspacesResponse struct {
	Workspaces []WorkspaceView `json:"workspaces"`
}

// handleRepoWorkspaces lists the workspaces the repo's manifests declare, so an
// app URL's workspace field can suggest names instead of asking for a guess.
func (s *Server) handleRepoWorkspaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	detected := agent.Workspaces(repo.Root)
	views := make([]WorkspaceView, 0, len(detected))
	for _, ws := range detected {
		views = append(views, WorkspaceView{Name: ws.Name, Path: ws.Path, DirName: ws.DirName})
	}
	writeJSON(w, http.StatusOK, WorkspacesResponse{Workspaces: views})
}
