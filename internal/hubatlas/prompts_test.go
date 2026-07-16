package hubatlas

import (
	"strings"
	"testing"
)

func TestCatalogViewsCarryPrompts(t *testing.T) {
	for _, v := range Catalog() {
		if strings.TrimSpace(v.Prompt) == "" {
			t.Errorf("view %q has no curated prompt", v.ID)
		}
		if schemaDoc(v.Flavor) == "" {
			t.Errorf("view %q flavor %q has no schema doc", v.ID, v.Flavor)
		}
	}
}

func TestGenerationPromptComposition(t *testing.T) {
	v, _ := ViewByID("data-model")

	first := v.GenerationPrompt("")
	for _, want := range []string{"read-only", "kebab-case", v.Prompt, "1:1", "N:M"} {
		if !strings.Contains(first, want) {
			t.Errorf("generation prompt missing %q", want)
		}
	}
	if strings.Contains(first, "Previous document") {
		t.Errorf("first-run prompt should not reference a previous document")
	}

	withPrev := v.GenerationPrompt(`{"entities":[{"id":"user"}]}`)
	if !strings.Contains(withPrev, "Previous document") || !strings.Contains(withPrev, `"user"`) {
		t.Errorf("prompt with a previous document should include it")
	}
}

func TestRetryPromptFeedsBackTheFailure(t *testing.T) {
	v, _ := ViewByID("app-flows")

	retry := v.RetryPrompt("", "not json", "invalid character 'o'")
	for _, want := range []string{"rejected", "invalid character 'o'", "not json", v.Prompt} {
		if !strings.Contains(retry, want) {
			t.Errorf("retry prompt missing %q", want)
		}
	}
}
