package prompts

import (
	"strings"
	"testing"
)

func mustLookup(t *testing.T, name string) Prompt {
	t.Helper()
	p, ok := Lookup(name)
	if !ok {
		t.Fatalf("Lookup(%q) missed a registry entry", name)
	}
	return p
}

func TestValidateOverrideAcceptsEveryDefault(t *testing.T) {
	for _, p := range Catalog() {
		if err := p.ValidateOverride(p.Default); err != nil {
			t.Errorf("%s: built-in default rejected: %v", p.Name, err)
		}
	}
}

func TestValidateOverrideRejectsDroppedRequiredPlaceholder(t *testing.T) {
	p := mustLookup(t, "verify")
	err := p.ValidateOverride("Verify {{.ID}} carefully and report back.")
	if err == nil {
		t.Fatal("template without {{.Verdict}} accepted")
	}
	if !strings.Contains(err.Error(), "{{.Verdict}}") {
		t.Fatalf("error %q does not name the missing placeholder", err)
	}
}

func TestValidateOverrideRejectsRequiredPlaceholderBehindOptionalBranch(t *testing.T) {
	p := mustLookup(t, "verify")
	err := p.ValidateOverride("Verify {{.ID}}. {{.SkillsNote}}{{if .Handoff}} Verdict to {{.Verdict}}.{{end}}")
	if err == nil {
		t.Fatal("template hiding {{.Verdict}} behind {{if .Handoff}} accepted")
	}
	if !strings.Contains(err.Error(), "{{.Verdict}}") {
		t.Fatalf("error %q does not name the missing placeholder", err)
	}
}

// TestValidateOverrideRejectsDroppedSkillsNote pins the template safety net: an
// override of a skill-carrying phase that omits {{.SkillsNote}} would silently
// run the agent with no skills instruction, so validation refuses it by name.
func TestValidateOverrideRejectsDroppedSkillsNote(t *testing.T) {
	bodies := map[string]string{
		"build":  "Implement {{.ID}} on branch {{.Branch}}.",
		"verify": "Verify {{.ID}} and write the verdict to {{.Verdict}}.",
		"repair": "Fix {{.Fails}} for {{.ID}} on {{.Branch}}; verdict at {{.Verdict}}.",
		"bugfix": "Fix every one of {{.Fails}} for {{.ID}} on {{.Branch}}; verdict at {{.Verdict}}.",
	}
	for name, body := range bodies {
		p := mustLookup(t, name)
		err := p.ValidateOverride(body)
		if err == nil {
			t.Errorf("%s: template without {{.SkillsNote}} accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "{{.SkillsNote}}") {
			t.Errorf("%s: error %q does not name the missing placeholder", name, err)
		}
	}
}

func TestValidateOverrideRejectsInterviewWithoutIssueBody(t *testing.T) {
	for _, name := range []string{"grill_issue", "grill_pregrill"} {
		p := mustLookup(t, name)
		err := p.ValidateOverride("Interview the user about {{.ID}} one question at a time.")
		if err == nil {
			t.Fatalf("%s: template without {{.Body}} accepted", name)
		}
		if !strings.Contains(err.Error(), "{{.Body}}") {
			t.Fatalf("%s: error %q does not name the missing placeholder", name, err)
		}
	}
}

func TestValidateOverrideRejectsParseError(t *testing.T) {
	p := mustLookup(t, "build")
	err := p.ValidateOverride("Implement {{.ID on branch {{.Branch}}.")
	if err == nil {
		t.Fatal("unparsable template accepted")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Fatalf("error %q does not read as a parse failure", err)
	}
}

func TestValidateOverrideRejectsUnknownPlaceholder(t *testing.T) {
	p := mustLookup(t, "commit")
	err := p.ValidateOverride("Commit {{.ID}} referencing {{.Bogus}}.")
	if err == nil {
		t.Fatal("template with an unknown placeholder accepted")
	}
	if !strings.Contains(err.Error(), "{{.Bogus}}") {
		t.Fatalf("error %q does not name the unknown placeholder", err)
	}
}

func TestValidateOverrideAcceptsRewordedBody(t *testing.T) {
	p := mustLookup(t, "commit")
	if err := p.ValidateOverride("Stage and commit the work for {{.ID}} in one commit."); err != nil {
		t.Fatalf("reworded body rejected: %v", err)
	}
}
