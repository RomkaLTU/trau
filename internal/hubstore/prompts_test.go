package hubstore

import (
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/prompts"
)

func testPromptOverrides(t *testing.T) *PromptOverrides {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewPromptOverrides(db.SQL())
}

func effectiveBody(t *testing.T, p *PromptOverrides, repo, name string) string {
	t.Helper()
	eff, err := p.Effective(repo)
	if err != nil {
		t.Fatalf("Effective(%q): %v", repo, err)
	}
	body, ok := eff[name]
	if !ok {
		t.Fatalf("Effective(%q) has no entry for %q", repo, name)
	}
	return body
}

func TestPromptOverridesPrecedence(t *testing.T) {
	p := testPromptOverrides(t)
	def, ok := prompts.Lookup("build")
	if !ok {
		t.Fatal("build prompt missing from registry")
	}

	if body := effectiveBody(t, p, "/repo/acme", "build"); body != def.Default {
		t.Fatalf("default-only effective body = %q, want the built-in default", body)
	}

	if err := p.Set("build", "", "global build body"); err != nil {
		t.Fatalf("set global: %v", err)
	}
	if body := effectiveBody(t, p, "/repo/acme", "build"); body != "global build body" {
		t.Fatalf("global-only effective body = %q, want the global override", body)
	}

	if err := p.Set("build", "/repo/acme", "repo build body"); err != nil {
		t.Fatalf("set repo: %v", err)
	}
	if body := effectiveBody(t, p, "/repo/acme", "build"); body != "repo build body" {
		t.Fatalf("effective body = %q, want the repo override to beat global", body)
	}
	if body := effectiveBody(t, p, "/repo/other", "build"); body != "global build body" {
		t.Fatalf("other repo effective body = %q, want the global override", body)
	}

	if err := p.Delete("build", "/repo/acme"); err != nil {
		t.Fatalf("delete repo: %v", err)
	}
	if body := effectiveBody(t, p, "/repo/acme", "build"); body != "global build body" {
		t.Fatalf("post-reset effective body = %q, want inheritance back to global", body)
	}
	if err := p.Delete("build", ""); err != nil {
		t.Fatalf("delete global: %v", err)
	}
	if body := effectiveBody(t, p, "/repo/acme", "build"); body != def.Default {
		t.Fatalf("post-reset effective body = %q, want inheritance back to the default", body)
	}
}

func TestPromptOverridesSetUpserts(t *testing.T) {
	p := testPromptOverrides(t)
	if err := p.Set("commit", "", "first"); err != nil {
		t.Fatalf("first set: %v", err)
	}
	if err := p.Set("commit", "", "second"); err != nil {
		t.Fatalf("second set: %v", err)
	}
	scope, err := p.Scope("")
	if err != nil {
		t.Fatalf("Scope: %v", err)
	}
	if scope["commit"] != "second" {
		t.Fatalf("scope body = %q, want the upserted second write", scope["commit"])
	}
}

func TestPromptOverridesDeleteAbsentIsNoop(t *testing.T) {
	p := testPromptOverrides(t)
	if err := p.Delete("commit", "/repo/acme"); err != nil {
		t.Fatalf("delete absent row: %v", err)
	}
}
