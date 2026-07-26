package webserver

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/teamsync"
)

// LessonView is one distilled lesson from a repo's durable ledger: the takeaway plus
// the context a browser shows — which ticket and phase produced it, the failure it
// came from, the evidence, how it ended, and when it was recorded. It is also the
// wire shape the loop child posts a new lesson in; Author and Me are read-only
// attribution the hub adds, so a POST that carries them is ignored. Me marks a
// record this machine wrote (ADR 0014).
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
	Author       string   `json:"author,omitempty"`
	Me           bool     `json:"me,omitempty"`
}

// LessonsResponse is the /api/v1/repos/{repo}/lessons resource: every distilled
// lesson the loop has recorded for the repo unioned with the ones teammates share
// over its git remote, most recent first.
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
		merged := mergeLessonViews(toLessonViews(lessons), s.teammateLessonViews(repo.Root))
		writeJSON(w, http.StatusOK, LessonsResponse{Repo: repo.Name, Lessons: merged})
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
		s.team.kick(repo.Root)
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

// teammateLessonViews reads the records teammates published for the repo, each
// attributed to the git name behind its writer id. A payload that no longer
// decodes is skipped: a teammate's stale snapshot never breaks the local read.
func (s *Server) teammateLessonViews(root string) []LessonView {
	records, err := s.stores.TeamSync().Records(root)
	if err != nil {
		logger.Verbosef("team sync: read records %s: %v", root, err)
		return nil
	}
	out := []LessonView{}
	for _, rec := range records {
		var p teamsync.Payload
		if err := json.Unmarshal([]byte(rec.Payload), &p); err != nil {
			continue
		}
		author := strings.TrimSpace(rec.AuthorName)
		if author == "" {
			author = rec.WriterID
		}
		for _, l := range p.Lessons {
			out = append(out, LessonView{
				Ticket:       l.Ticket,
				Phase:        l.Phase,
				FailureType:  l.FailureType,
				AttemptedFix: l.AttemptedFix,
				Evidence:     l.Evidence,
				Result:       l.Result,
				Lesson:       l.Lesson,
				Tags:         l.Tags,
				RecordedAt:   l.RecordedAt,
				Author:       author,
			})
		}
	}
	return out
}

// mergeLessonViews unions the repo's own records with the ones teammates share,
// most recent first, dropping a teammate record whose takeaway a local one already
// carries. Local records win every tie and keep their order among themselves, so
// sync only ever adds to what this machine recorded. RecordedAt is RFC3339 in UTC,
// which orders lexicographically; a record with none sorts last.
func mergeLessonViews(local, teammates []LessonView) []LessonView {
	for i := range local {
		local[i].Me = true
	}
	if len(teammates) == 0 {
		return local
	}
	seen := make(map[string]bool, len(local))
	merged := make([]LessonView, 0, len(local)+len(teammates))
	for _, v := range local {
		seen[strings.TrimSpace(v.Lesson)] = true
		merged = append(merged, v)
	}
	for _, v := range teammates {
		key := strings.TrimSpace(v.Lesson)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, v)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].RecordedAt > merged[j].RecordedAt
	})
	return merged
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
