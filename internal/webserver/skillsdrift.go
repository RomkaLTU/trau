package webserver

import (
	"net/http"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/registry"
)

// SkillDriftView is one locked skill whose installed state does not match the
// pin.
type SkillDriftView struct {
	Name          string `json:"name"`
	Class         string `json:"class"`
	Source        string `json:"source,omitempty"`
	SourceType    string `json:"source_type,omitempty"`
	SkillPath     string `json:"skill_path,omitempty"`
	PinnedHash    string `json:"pinned_hash,omitempty"`
	InstalledHash string `json:"installed_hash,omitempty"`
}

// SkillLockView is the repo's standing against skills-lock.json. Checked is
// false for a repo that pins nothing, which reads as silent rather than clean.
type SkillLockView struct {
	Checked  bool             `json:"checked"`
	Drift    []SkillDriftView `json:"drift"`
	Unlocked []string         `json:"unlocked,omitempty"`
}

// SkillInstallFailure is one skill the install gesture refused: the fetch
// failed, or its content did not hash to the pin and was rolled back.
type SkillInstallFailure struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// SkillDriftInstallResponse is the outcome of the install gesture. Failures are
// carried beside the successes rather than collapsing the gesture into an error
// status, so the panel can name each refusal and still re-render.
type SkillDriftInstallResponse struct {
	Installed []string              `json:"installed"`
	Failed    []SkillInstallFailure `json:"failed,omitempty"`
	Skills    SkillsResponse        `json:"skills"`
}

func skillLockView(repoRoot string) SkillLockView {
	report := agent.CheckSkillDrift(repoRoot)
	view := SkillLockView{Checked: report.Checked, Drift: make([]SkillDriftView, 0, len(report.Drifted)), Unlocked: report.Unlocked}
	for _, d := range report.Drifted {
		view.Drift = append(view.Drift, SkillDriftView{
			Name:          d.Name,
			Class:         d.Class,
			Source:        d.Lock.Source,
			SourceType:    d.Lock.SourceType,
			SkillPath:     d.Lock.SkillPath,
			PinnedHash:    d.Lock.Hash,
			InstalledHash: d.Installed,
		})
	}
	return view
}

// handleSkillDrift is the explicit install gesture: it installs every drifted
// skill from its pinned source and keeps only content that hashes to the lock's
// computedHash. Nothing else in the hub installs a skill, because an install
// writes prompt-shaping instructions onto the machine.
func (s *Server) handleSkillDrift(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	report := agent.CheckSkillDrift(repo.Root)
	if !report.Checked {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this repo has no skills-lock.json to install from"})
		return
	}
	writeJSON(w, http.StatusOK, s.installDrifted(r, repo, report))
}

func (s *Server) installDrifted(r *http.Request, repo registry.Repo, report agent.SkillDriftReport) SkillDriftInstallResponse {
	out := SkillDriftInstallResponse{Installed: []string{}}
	for _, d := range report.Drifted {
		if err := agent.InstallPinnedSkill(r.Context(), repo.Root, d.Name, d.Lock, s.installSkill); err != nil {
			out.Failed = append(out.Failed, SkillInstallFailure{Name: d.Name, Error: err.Error()})
			continue
		}
		out.Installed = append(out.Installed, d.Name)
	}
	out.Skills = s.skillsSnapshot(repo)
	return out
}
