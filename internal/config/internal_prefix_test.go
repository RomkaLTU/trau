package config

import "testing"

func TestInternalPrefix(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		repo       string
		want       string
	}{
		{"explicit configured prefix wins", "eng", "my-app", "ENG"},
		{"derives from the repo name", "", "loop", "LOOP"},
		{"sanitizes punctuation out of the repo name", "", "my-cool.app", "MYCOOLAPP"},
		{"drops leading digits so the id starts with a letter", "", "3d-tool", "DTOOL"},
		{"configured prefix is sanitized too", "co-d", "loop", "COD"},
		{"falls back when nothing usable survives", "", "---", "ISSUE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InternalPrefix(tc.configured, tc.repo); got != tc.want {
				t.Fatalf("InternalPrefix(%q, %q) = %q, want %q", tc.configured, tc.repo, got, tc.want)
			}
		})
	}
}

func TestIssuePrefixConfiguredCapturesExplicitValue(t *testing.T) {
	t.Setenv("TRAU_ISSUE_PREFIX", "")
	t.Setenv("ISSUE_PREFIX", "eng")
	c, err := LoadLayered("", "", "", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.IssuePrefixConfigured != "ENG" {
		t.Fatalf("IssuePrefixConfigured = %q, want ENG", c.IssuePrefixConfigured)
	}
}

func TestIssuePrefixConfiguredEmptyWhenDerivedFromTeam(t *testing.T) {
	t.Setenv("TRAU_ISSUE_PREFIX", "")
	t.Setenv("ISSUE_PREFIX", "")
	t.Setenv("TRAU_LINEAR_TEAM", "")
	t.Setenv("LINEAR_TEAM", "COD")
	c, err := LoadLayered("", "", "", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.IssuePrefixConfigured != "" {
		t.Fatalf("IssuePrefixConfigured = %q, want empty when only the team is set", c.IssuePrefixConfigured)
	}
	if c.IssuePrefix != "COD" {
		t.Fatalf("IssuePrefix = %q, want the team-derived COD for external ticket parsing", c.IssuePrefix)
	}
}
