package planning

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Status is the kind of result the planning agent returned. It is the discriminant
// of the payload: exactly one of the three shapes is populated per round.
type Status string

const (
	// StatusQuestions asks the user structured questions before a PRD can be drafted.
	StatusQuestions Status = "questions"
	// StatusPRD carries a finished PRD ready to render and persist.
	StatusPRD Status = "prd"
	// StatusSlices carries the tracer-bullet slices a published PRD was broken into.
	StatusSlices Status = "slices"
)

// Payload is the machine protocol between the planning agent and trau, written to
// the result-file channel as JSON. Status selects which of Questions/PRD/Slices is
// meaningful; the other fields are empty. This shape is the protocol for the whole
// planning module — later slices add the question and slice rounds on top of it.
type Payload struct {
	Status    Status     `json:"status"`
	Questions []Question `json:"questions,omitempty"`
	PRD       *PRD       `json:"prd,omitempty"`
	Slices    []Slice    `json:"slices,omitempty"`
}

// Question is one structured question the agent asks through the payload rather
// than in prose, mirroring the AskUserQuestion contract the TUI already renders.
type Question struct {
	ID         string   `json:"id"`
	Header     string   `json:"header"`
	Text       string   `json:"text"`
	Options    []Option `json:"options"`
	Multi      bool     `json:"multi"`
	AllowOther bool     `json:"allow_other"`
	Default    string   `json:"default"`
}

// Option is one selectable answer to a Question.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// PRD is a rendered product requirements document.
type PRD struct {
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
}

// Slice is one independently-grabbable unit of work a PRD was broken into.
type Slice struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
	After       []string `json:"after"`
}

// Parse reads a planning payload from the agent's result-file text and validates
// it. The text may be wrapped in a Markdown code fence — the agent writes to a
// freeform file — so the outermost JSON object is extracted first. A malformed
// object, an unknown status, or a status missing its required fields is an error.
func Parse(raw string) (Payload, error) {
	body := extractJSON(raw)
	if body == "" {
		return Payload{}, fmt.Errorf("planning payload: no JSON object found")
	}
	var p Payload
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		return Payload{}, fmt.Errorf("planning payload: %w", err)
	}
	if err := p.validate(); err != nil {
		return Payload{}, err
	}
	return p, nil
}

func (p Payload) validate() error {
	switch p.Status {
	case StatusQuestions:
		if len(p.Questions) == 0 {
			return fmt.Errorf("planning payload: status %q has no questions", p.Status)
		}
		for i, q := range p.Questions {
			if strings.TrimSpace(q.ID) == "" || strings.TrimSpace(q.Text) == "" {
				return fmt.Errorf("planning payload: question %d missing id or text", i)
			}
		}
	case StatusPRD:
		if p.PRD == nil || strings.TrimSpace(p.PRD.Markdown) == "" {
			return fmt.Errorf("planning payload: status %q missing prd markdown", p.Status)
		}
	case StatusSlices:
		if len(p.Slices) == 0 {
			return fmt.Errorf("planning payload: status %q has no slices", p.Status)
		}
		for i, s := range p.Slices {
			if strings.TrimSpace(s.Title) == "" {
				return fmt.Errorf("planning payload: slice %d missing title", i)
			}
		}
	case "":
		return fmt.Errorf("planning payload: missing status")
	default:
		return fmt.Errorf("planning payload: unknown status %q", p.Status)
	}
	return nil
}

// extractJSON returns the outermost {...} object in raw, tolerating a surrounding
// Markdown code fence and any prose the agent wrapped it in. It returns "" when no
// balanced object is present, letting Parse report a clear "no JSON" error.
func extractJSON(raw string) string {
	s := strings.TrimSpace(raw)
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}
