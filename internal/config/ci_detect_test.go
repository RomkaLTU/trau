package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasPullRequestCI(t *testing.T) {
	cases := []struct {
		name     string
		files    map[string]string
		wantTrue bool
	}{
		{name: "no workflows dir", files: nil, wantTrue: false},
		{
			name:     "push-only deploy workflow",
			files:    map[string]string{"deploy.yml": "name: deploy\non:\n  push:\n    branches: [main, dev]\n"},
			wantTrue: false,
		},
		{
			name:     "pull_request map form",
			files:    map[string]string{"ci.yml": "on:\n  pull_request:\n    branches: [main]\n"},
			wantTrue: true,
		},
		{
			name:     "pull_request list form",
			files:    map[string]string{"ci.yml": "on: [push, pull_request]\n"},
			wantTrue: true,
		},
		{
			name:     "pull_request scalar form",
			files:    map[string]string{"ci.yaml": "on: pull_request\n"},
			wantTrue: true,
		},
		{
			name:     "pull_request_target",
			files:    map[string]string{"ci.yml": "on:\n  pull_request_target:\n"},
			wantTrue: true,
		},
		{
			name: "one of several workflows has PR trigger",
			files: map[string]string{
				"deploy.yml": "on:\n  push:\n    branches: [main]\n",
				"test.yml":   "on:\n  pull_request:\n",
			},
			wantTrue: true,
		},
		{
			name:     "pull_request only mentioned in a step condition, not a trigger",
			files:    map[string]string{"deploy.yml": "on:\n  push:\njobs:\n  x:\n    steps:\n      - if: github.event_name == 'pull_request'\n        run: echo hi\n"},
			wantTrue: false,
		},
		{
			name:     "non-yaml file ignored",
			files:    map[string]string{"README.md": "on: pull_request\n"},
			wantTrue: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.files != nil {
				wf := filepath.Join(root, ".github", "workflows")
				if err := os.MkdirAll(wf, 0o755); err != nil {
					t.Fatal(err)
				}
				for name, body := range tc.files {
					if err := os.WriteFile(filepath.Join(wf, name), []byte(body), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if got := HasPullRequestCI(root); got != tc.wantTrue {
				t.Fatalf("HasPullRequestCI = %v, want %v", got, tc.wantTrue)
			}
		})
	}

	if HasPullRequestCI("") {
		t.Fatal("empty repoRoot must be false")
	}
}

func TestScanPullRequestCI(t *testing.T) {
	cases := []struct {
		name            string
		files           map[string]string
		wantHasPR       bool
		wantAllFiltered bool
	}{
		{name: "no workflows dir", files: nil},
		{
			name:            "unfiltered pull_request",
			files:           map[string]string{"ci.yml": "on:\n  pull_request:\n    branches: [main]\n"},
			wantHasPR:       true,
			wantAllFiltered: false,
		},
		{
			name:            "paths filter",
			files:           map[string]string{"ci.yml": "on:\n  pull_request:\n    paths:\n      - 'web/**'\n"},
			wantHasPR:       true,
			wantAllFiltered: true,
		},
		{
			name:            "paths-ignore filter",
			files:           map[string]string{"ci.yml": "on:\n  pull_request:\n    paths-ignore:\n      - 'docs/**'\n"},
			wantHasPR:       true,
			wantAllFiltered: true,
		},
		{
			name: "one filtered one not",
			files: map[string]string{
				"web.yml":  "on:\n  pull_request:\n    paths:\n      - 'web/**'\n",
				"test.yml": "on:\n  pull_request:\n",
			},
			wantHasPR:       true,
			wantAllFiltered: false,
		},
		{
			name: "filtered PR workflow plus push-only workflow",
			files: map[string]string{
				"web.yml":    "on:\n  pull_request:\n    paths:\n      - 'web/**'\n",
				"deploy.yml": "on:\n  push:\n    branches: [main]\n",
			},
			wantHasPR:       true,
			wantAllFiltered: true,
		},
		{
			name:            "sequence form cannot carry a filter",
			files:           map[string]string{"ci.yml": "on: [push, pull_request]\n"},
			wantHasPR:       true,
			wantAllFiltered: false,
		},
		{
			name:            "scalar form cannot carry a filter",
			files:           map[string]string{"ci.yml": "on: pull_request\n"},
			wantHasPR:       true,
			wantAllFiltered: false,
		},
		{
			name:            "pull_request_target with paths",
			files:           map[string]string{"ci.yml": "on:\n  pull_request_target:\n    paths:\n      - 'src/**'\n"},
			wantHasPR:       true,
			wantAllFiltered: true,
		},
		{
			name:            "filtered pull_request but unfiltered pull_request_target in one workflow",
			files:           map[string]string{"ci.yml": "on:\n  pull_request:\n    paths:\n      - 'web/**'\n  pull_request_target:\n"},
			wantHasPR:       true,
			wantAllFiltered: false,
		},
		{
			name:            "push with paths is not a PR trigger",
			files:           map[string]string{"deploy.yml": "on:\n  push:\n    paths:\n      - 'infra/**'\n"},
			wantHasPR:       false,
			wantAllFiltered: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.files != nil {
				wf := filepath.Join(root, ".github", "workflows")
				if err := os.MkdirAll(wf, 0o755); err != nil {
					t.Fatal(err)
				}
				for name, body := range tc.files {
					if err := os.WriteFile(filepath.Join(wf, name), []byte(body), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			got := ScanPullRequestCI(root)
			if got.HasPRWorkflows != tc.wantHasPR || got.AllPathFiltered != tc.wantAllFiltered {
				t.Fatalf("ScanPullRequestCI = %+v, want {HasPRWorkflows:%v AllPathFiltered:%v}", got, tc.wantHasPR, tc.wantAllFiltered)
			}
		})
	}
}
