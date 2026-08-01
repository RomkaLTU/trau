package main

import (
	"slices"
	"testing"
)

func TestUncovered(t *testing.T) {
	cases := []struct {
		name string
		pkgs []pkg
		want []string
	}{
		{
			name: "tests importing the guard",
			pkgs: []pkg{{
				ImportPath:  modulePath + "/internal/agent",
				TestGoFiles: []string{"agent_test.go"},
				TestImports: []string{"testing", guardPath},
			}},
			want: []string{},
		},
		{
			name: "tests missing the guard",
			pkgs: []pkg{{
				ImportPath:  modulePath + "/internal/agent",
				TestGoFiles: []string{"agent_test.go"},
				TestImports: []string{"testing"},
			}},
			want: []string{modulePath + "/internal/agent"},
		},
		{
			name: "external test package importing the guard",
			pkgs: []pkg{{
				ImportPath:   modulePath + "/internal/agent",
				XTestGoFiles: []string{"agent_ext_test.go"},
				XTestImports: []string{"testing", guardPath},
			}},
			want: []string{},
		},
		{
			name: "package with no tests",
			pkgs: []pkg{{ImportPath: modulePath + "/internal/logger"}},
			want: []string{},
		},
		{
			name: "the guard package itself",
			pkgs: []pkg{{
				ImportPath:  guardPath,
				TestGoFiles: []string{"netguard_test.go"},
				TestImports: []string{"testing"},
			}},
			want: []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := paths(uncovered(tc.pkgs)); !slices.Equal(got, tc.want) {
				t.Errorf("uncovered = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLeaked(t *testing.T) {
	cases := []struct {
		name string
		pkgs []pkg
		want []string
	}{
		{
			name: "production import of the guard",
			pkgs: []pkg{{
				ImportPath: modulePath + "/internal/hubclient",
				Deps:       []string{"net/http", guardPath},
			}},
			want: []string{modulePath + "/internal/hubclient"},
		},
		{
			name: "transitive production import of the guard",
			pkgs: []pkg{{
				ImportPath: modulePath + "/cmd/trau",
				Deps:       []string{modulePath + "/internal/hubclient", guardPath},
			}},
			want: []string{modulePath + "/cmd/trau"},
		},
		{
			name: "test-only import of the guard",
			pkgs: []pkg{{
				ImportPath:  modulePath + "/internal/hubclient",
				TestGoFiles: []string{"hubclient_test.go"},
				TestImports: []string{guardPath},
				Deps:        []string{"net/http"},
			}},
			want: []string{},
		},
		{
			name: "the guard package itself",
			pkgs: []pkg{{ImportPath: guardPath, Deps: []string{"net/http"}}},
			want: []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := paths(leaked(tc.pkgs)); !slices.Equal(got, tc.want) {
				t.Errorf("leaked = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRelPath(t *testing.T) {
	if got := relPath(pkg{ImportPath: modulePath + "/internal/agent"}); got != "internal/agent" {
		t.Errorf("relPath = %q, want internal/agent", got)
	}
}

func paths(pkgs []pkg) []string {
	out := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, p.ImportPath)
	}
	return out
}
