package prompts

import (
	"strings"
	"testing"
)

func TestRendererUsesOverrideBody(t *testing.T) {
	r := Renderer{Overrides: map[string]string{"lint_fix": "Short lint pass for {{.ID}}."}}
	if got := r.Render("lint_fix", LintFixData{ID: "COD-1"}); got != "Short lint pass for COD-1." {
		t.Fatalf("Render = %q", got)
	}
}

func TestRendererWithoutOverrideMatchesDefault(t *testing.T) {
	data := LintFixData{ID: "COD-1"}
	if got, want := (Renderer{}).Render("lint_fix", data), Render("lint_fix", data); got != want {
		t.Fatalf("Render = %q, want the built-in default %q", got, want)
	}
}

func TestRendererBrokenOverrideFallsBackAndReports(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"parse failure", "unclosed {{.ID"},
		{"execute failure", "unknown {{.NoSuchField}}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reported string
			r := Renderer{
				Overrides:       map[string]string{"lint_fix": tc.body},
				OnOverrideError: func(name string, err error) { reported = name + ": " + err.Error() },
			}
			data := LintFixData{ID: "COD-1"}
			if got, want := r.Render("lint_fix", data), Render("lint_fix", data); got != want {
				t.Fatalf("Render = %q, want the built-in default %q", got, want)
			}
			if !strings.HasPrefix(reported, "lint_fix: ") {
				t.Fatalf("OnOverrideError reported %q, want the prompt name and error", reported)
			}
		})
	}
}
