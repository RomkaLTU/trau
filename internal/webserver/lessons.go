package webserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// LessonView is one distilled lesson from a repo's durable ledger
// (runs/memory/lessons.jsonl): the takeaway plus the context a browser shows —
// which ticket and phase produced it, the failure it came from, the evidence,
// how it ended, and when it was recorded.
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

func (s *Server) handleLessons(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, LessonsResponse{Repo: repo.Name, Lessons: readLessons(lessonsPath(repo.RunsDir))})
}

// readLessons parses the repo's append-only JSONL ledger into a browsable list,
// newest first, skipping any blank or malformed line so a single corrupt record
// never breaks the page. A missing ledger reads as an empty list — a repo the loop
// has not taught yet, not an error.
func readLessons(path string) []LessonView {
	out := []LessonView{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var l LessonView
		if err := json.Unmarshal(line, &l); err != nil {
			continue
		}
		if strings.TrimSpace(l.Lesson) == "" {
			continue
		}
		out = append(out, l)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func lessonsPath(runsDir string) string {
	return filepath.Join(runsDir, "memory", "lessons.jsonl")
}
