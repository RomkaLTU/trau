package webserver

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// LessonView is one distilled lesson from a repo's durable ledger: the takeaway plus
// the context a browser shows — which ticket and phase produced it, the failure it
// came from, the evidence, how it ended, and when it was recorded. It is also the
// wire shape the loop child posts a new lesson in.
type LessonView struct {
	Ticket       string   `json:"ticket,omitempty"`
	Phase        string   `json:"phase,omitempty"`
	FailureType  string   `json:"failure_type,omitempty"`
	AttemptedFix string   `json:"attempted_fix,omitempty"`
	Evidence     []string `json:"evidence,omitempty"`
	Result       string   `json:"result,omitempty"`
	Lesson       string   `json:"lesson"`
	Tags         []string `json:"tags,omitempty"`
	RecordedAt   string   `json:"recorded_at,omitempty"`
}

// LessonsResponse is the /api/v1/repos/{repo}/lessons resource: every distilled
// lesson the loop has recorded for the repo, most recent first.
type LessonsResponse struct {
	Repo    string       `json:"repo"`
	Lessons []LessonView `json:"lessons"`
}

// handleLessons is the loop child's read/write seam for a repo's durable lessons
// ledger and the browser's read of the same (COD-529, ADR 0008). The child posts a
// distilled lesson with POST and recalls the recorded ones with GET; the child never
// opens the database. On first touch of a repo any file-era ledger is folded in.
func (s *Server) handleLessons(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	s.importLessons(repo)
	store := s.stores.Lessons()

	switch r.Method {
	case http.MethodGet:
		lessons, err := store.All(repo.Root)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, LessonsResponse{Repo: repo.Name, Lessons: toLessonViews(lessons)})
	case http.MethodPost:
		var lv LessonView
		if err := json.NewDecoder(r.Body).Decode(&lv); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		if strings.TrimSpace(lv.Lesson) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty lesson"})
			return
		}
		if err := store.Append(repo.Root, lessonFromView(lv)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// importLessons folds a repo's file-era ledger into the authoritative table on first
// touch, best-effort. Like importArtifacts it skips a repo with a live loop — a
// legacy loop mid-migration may still be appending its file, so the hub never touches
// a live run's state — and leaves the file in place to retry on the next touch when
// an import fails.
func (s *Server) importLessons(repo registry.Repo) {
	if _, live := s.liveInstance(repo.Root); live {
		return
	}
	runsDir := repo.RunsDir
	if runsDir == "" {
		runsDir = repoRunsDir(repo.Root)
	}
	if err := s.stores.Lessons().ImportLegacy(repo.Root, runsDir); err != nil {
		logger.Verbosef("import legacy lessons %s: %v", repo.Name, err)
	}
}

// importAllLessons folds every known repo's file-era ledger into the table, off any
// request path — the serve-startup counterpart to the per-repo lazy import.
func (s *Server) importAllLessons() {
	for _, repo := range s.knownRepos(s.liveInstances()) {
		s.importLessons(repo)
	}
}

func toLessonViews(lessons []hubstore.Lesson) []LessonView {
	out := make([]LessonView, len(lessons))
	for i, l := range lessons {
		out[i] = LessonView{
			Ticket:       l.Ticket,
			Phase:        l.Phase,
			FailureType:  l.FailureType,
			AttemptedFix: l.AttemptedFix,
			Evidence:     l.Evidence,
			Result:       l.Result,
			Lesson:       l.Lesson,
			Tags:         l.Tags,
			RecordedAt:   l.RecordedAt,
		}
	}
	return out
}

func lessonFromView(v LessonView) hubstore.Lesson {
	return hubstore.Lesson{
		Ticket:       v.Ticket,
		Phase:        v.Phase,
		FailureType:  v.FailureType,
		AttemptedFix: v.AttemptedFix,
		Evidence:     v.Evidence,
		Result:       v.Result,
		Lesson:       v.Lesson,
		Tags:         v.Tags,
		RecordedAt:   v.RecordedAt,
	}
}
