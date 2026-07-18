// Package prompts holds every configurable pipeline prompt as a named
// text/template with a typed data struct, plus per-prompt metadata. It is a
// leaf package (no internal imports) so config, pipeline, and webserver can
// all render from the same registry.
package prompts

import (
	"strings"
	"text/template"
)

// Placeholder describes one field a prompt's data struct exposes to its
// template. Required marks the fields carrying pipeline contracts — file
// paths the loop reads back, schemas it parses, or identifiers the phase
// cannot work without. Sample is a representative value override validation
// renders with; a required field's sample doubles as the sentinel proving
// the template actually references it.
type Placeholder struct {
	Field       string
	Description string
	Required    bool
	Sample      any
}

// Prompt is one registry entry: a stable snake_case name, human-facing
// metadata, the placeholder list, and the built-in default template body.
type Prompt struct {
	Name         string
	Title        string
	Description  string
	Placeholders []Placeholder
	Default      string
}

// SkillsData feeds the skills prompt. Required must already be intersected
// with Installed — names that cannot be loaded are the caller's to drop.
type SkillsData struct {
	Installed []string
	Required  []string
}

// BuildData feeds the build prompt. SkillsNote, Note, CodeStyle, and
// BuildNotes are pre-rendered fragments; TicketContext is the injected
// ticket block.
type BuildData struct {
	ID            string
	Branch        string
	SkillsNote    string
	Note          string
	CodeStyle     string
	BuildNotes    string
	TicketContext string
}

// HandoffData feeds the handoff prompt. Handoff is the QA-brief file path;
// Rubric is the pre-rendered rubric request fragment.
type HandoffData struct {
	ID            string
	Handoff       string
	Rubric        string
	TicketContext string
}

// VerifyData feeds the verify prompt. Verdict is the JSON verdict file path;
// an empty Handoff switches the template to its derive-from-ticket wording.
type VerifyData struct {
	ID             string
	Handoff        string
	Verdict        string
	Note           string
	ChecksFragment string
	RubricNote     string
	LessonsNote    string
	TicketContext  string
}

// CommitData feeds the commit prompt. Squash selects the skip-splitting
// sentence for squash-merge repos.
type CommitData struct {
	ID         string
	RubricNote string
	Squash     bool
}

// RepairData feeds both the repair and bugfix prompts. An empty Handoff drops
// the QA-brief reference in favor of the injected ticket content.
type RepairData struct {
	ID            string
	Verdict       string
	Handoff       string
	Branch        string
	Fails         string
	RubricNote    string
	LessonsNote   string
	NotesNote     string
	CodeStyle     string
	TicketContext string
}

// PushRepairData feeds the push_repair prompt. HookOutput is the verbatim
// pre-push rejection.
type PushRepairData struct {
	ID         string
	HookOutput string
	NotesNote  string
	CodeStyle  string
}

// ResolveConflictsData feeds the resolve_conflicts prompt.
type ResolveConflictsData struct {
	ID     string
	Base   string
	Branch string
}

// EpicRepairData feeds the epic_repair prompt.
type EpicRepairData struct {
	EpicID string
	PRURL  string
	Branch string
}

// CleanupData feeds the cleanup prompt.
type CleanupData struct {
	ID        string
	NotesNote string
}

// LintFixData feeds the lint_fix prompt.
type LintFixData struct {
	ID string
}

// LessonsDistillData feeds the lessons_distill prompt. Path is the JSON file
// the loop parses back; Schema is the exact JSON skeleton to fill.
type LessonsDistillData struct {
	ID          string
	Result      string
	FailureType string
	Evidence    string
	Path        string
	Schema      string
}

// RubricData feeds the rubric fragment appended to the handoff prompt. Path
// is the rubric JSON file the loop reads back; Schema is its exact shape.
type RubricData struct {
	ID     string
	Path   string
	Schema string
}

// BuildNotesData feeds the build_notes fragment appended to the build prompt.
// Path is the notes file the mechanical phases read back.
type BuildNotesData struct {
	ID   string
	Path string
}

// TimelogEstimateData feeds the timelog_estimate prompt. Path is the file the
// loop parses the integer estimate from.
type TimelogEstimateData struct {
	ID        string
	Files     int
	Additions int
	Deletions int
	Commits   int
	Path      string
}

var templateFuncs = template.FuncMap{"join": strings.Join}

var templates = func() map[string]*template.Template {
	m := make(map[string]*template.Template, len(registry))
	for _, p := range registry {
		m[p.Name] = template.Must(template.New(p.Name).Funcs(templateFuncs).Parse(p.Default))
	}
	return m
}()

// Render executes the named prompt's default template over data. Unknown
// names and execution failures panic: the built-in templates and their typed
// data structs are compile-time fixtures, so either is a programming error.
func Render(name string, data any) string {
	t, ok := templates[name]
	if !ok {
		panic("prompts: unknown prompt " + name)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		panic("prompts: render " + name + ": " + err.Error())
	}
	return b.String()
}

// Renderer renders prompts with stored override bodies layered over the
// built-in defaults. The zero value renders defaults only. An override that
// fails to parse or execute is reported through OnOverrideError (when set)
// and the built-in default renders instead — a broken override never breaks
// a render.
type Renderer struct {
	Overrides       map[string]string
	OnOverrideError func(name string, err error)
}

// Render executes the override body stored for name over data, falling back
// to the named built-in template when no override is stored or the override
// fails.
func (r Renderer) Render(name string, data any) string {
	body, ok := r.Overrides[name]
	if !ok {
		return Render(name, data)
	}
	out, err := renderBody(name, body, data)
	if err != nil {
		if r.OnOverrideError != nil {
			r.OnOverrideError(name, err)
		}
		return Render(name, data)
	}
	return out
}

func renderBody(name, body string, data any) (string, error) {
	t, err := template.New(name).Funcs(templateFuncs).Parse(body)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Catalog returns the registry's per-prompt metadata in stable order.
func Catalog() []Prompt {
	out := make([]Prompt, len(registry))
	copy(out, registry)
	return out
}

// Lookup returns the registry entry for name.
func Lookup(name string) (Prompt, bool) {
	for _, p := range registry {
		if p.Name == name {
			return p, true
		}
	}
	return Prompt{}, false
}
