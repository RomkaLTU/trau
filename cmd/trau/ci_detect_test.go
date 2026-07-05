package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseProtectionChecks(t *testing.T) {
	cases := []struct {
		name string
		data string
		want []string
	}{
		{name: "modern checks shape", data: `{"checks":[{"context":"build"},{"context":"test"}],"contexts":[]}`, want: []string{"build", "test"}},
		{name: "deprecated contexts shape", data: `{"contexts":["lint"]}`, want: []string{"lint"}},
		{name: "both shapes", data: `{"contexts":["lint"],"checks":[{"context":"build"}]}`, want: []string{"lint", "build"}},
		{name: "empty", data: `{"contexts":[],"checks":[]}`, want: nil},
		{name: "invalid json", data: `not json`, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseProtectionChecks([]byte(tc.data))
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseProtectionChecks(%s) = %v, want %v", tc.data, got, tc.want)
			}
		})
	}
}

func TestParseRulesetChecks(t *testing.T) {
	cases := []struct {
		name string
		data string
		want []string
	}{
		{
			name: "required_status_checks rule",
			data: `[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"ci/build"},{"context":"ci/test"}]}},{"type":"pull_request"}]`,
			want: []string{"ci/build", "ci/test"},
		},
		{name: "no matching rule", data: `[{"type":"pull_request"},{"type":"deletion"}]`, want: nil},
		{name: "empty array", data: `[]`, want: nil},
		{name: "invalid json", data: `{}`, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRulesetChecks([]byte(tc.data))
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseRulesetChecks(%s) = %v, want %v", tc.data, got, tc.want)
			}
		})
	}
}

func TestDedupeChecks(t *testing.T) {
	got := dedupeChecks([]string{"a", " a ", "", "b", "a", "  "})
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dedupeChecks = %v, want %v", got, want)
	}
}

// TestDetectCIGateFallback covers the local-workflow fallback: a bare directory
// (no GitHub remote) forces detectCIGate past the gh path, so the result comes
// from HasPullRequestCI alone — deterministic whether or not gh is installed.
func TestDetectCIGateFallback(t *testing.T) {
	cases := []struct {
		name       string
		workflow   string
		wantGate   bool
		wantSource string
	}{
		{name: "pull_request workflow", workflow: "on:\n  pull_request:\n    branches: [main]\n", wantGate: true, wantSource: "workflows"},
		{name: "push-only workflow", workflow: "on:\n  push:\n    branches: [main]\n", wantGate: false, wantSource: "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			wf := filepath.Join(dir, ".github", "workflows")
			if err := os.MkdirAll(wf, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wf, "ci.yml"), []byte(tc.workflow), 0o644); err != nil {
				t.Fatal(err)
			}
			got := detectCIGate(context.Background(), dir, "")
			if got.Gate != tc.wantGate || got.Source != tc.wantSource {
				t.Fatalf("detectCIGate = {Gate:%v Source:%q}, want {Gate:%v Source:%q}", got.Gate, got.Source, tc.wantGate, tc.wantSource)
			}
			if got.Confident {
				t.Fatalf("fallback result must not be Confident")
			}
		})
	}

	if got := detectCIGate(context.Background(), "", ""); got.Gate || got.Source != "none" {
		t.Fatalf("empty repoRoot = %+v, want gate off / source none", got)
	}
}
