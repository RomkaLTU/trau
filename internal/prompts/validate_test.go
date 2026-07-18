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
	err := p.ValidateOverride("Verify {{.ID}}.{{if .Handoff}} Verdict to {{.Verdict}}.{{end}}")
	if err == nil {
		t.Fatal("template hiding {{.Verdict}} behind {{if .Handoff}} accepted")
	}
	if !strings.Contains(err.Error(), "{{.Verdict}}") {
		t.Fatalf("error %q does not name the missing placeholder", err)
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
