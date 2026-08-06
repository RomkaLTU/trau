package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writePipelines(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, BitbucketPipelinesFile), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", BitbucketPipelinesFile, err)
	}
	return dir
}

// TestScanBitbucketPipelinesTellsPRCIFromNone is the whole point of the scan: a
// repo whose pipelines can never answer a pull request must be waivable by the
// merge gate, and one whose pipelines can must never be waived.
func TestScanBitbucketPipelinesTellsPRCIFromNone(t *testing.T) {
	cases := []struct {
		name           string
		body           string
		wantPR         bool
		wantPathFilter bool
	}{
		{
			name:   "pull-requests section",
			body:   "pipelines:\n  pull-requests:\n    '**':\n      - step:\n          script: [make test]\n",
			wantPR: true,
		},
		{
			name:   "default pipeline",
			body:   "pipelines:\n  default:\n    - step:\n        script: [make test]\n",
			wantPR: true,
		},
		{
			name:   "branches only",
			body:   "pipelines:\n  branches:\n    main:\n      - step:\n          script: [make deploy]\n",
			wantPR: true,
		},
		{
			name:   "custom and tags cannot answer a pull request",
			body:   "pipelines:\n  custom:\n    release:\n      - step:\n          script: [make dist]\n  tags:\n    'v*':\n      - step:\n          script: [make dist]\n",
			wantPR: false,
		},
		{
			name:   "no pipelines key",
			body:   "image: golang:1.25\n",
			wantPR: false,
		},
		{
			name:   "unreadable yaml",
			body:   "pipelines: [\n",
			wantPR: false,
		},
		{
			name:           "every step conditioned on paths",
			body:           "pipelines:\n  pull-requests:\n    '**':\n      - step:\n          condition:\n            changesets:\n              includePaths: [web/**]\n          script: [make web]\n",
			wantPR:         true,
			wantPathFilter: true,
		},
		{
			name:           "one unconditioned step among conditioned ones",
			body:           "pipelines:\n  pull-requests:\n    '**':\n      - step:\n          condition:\n            changesets:\n              includePaths: [web/**]\n          script: [make web]\n      - step:\n          script: [make test]\n",
			wantPR:         true,
			wantPathFilter: false,
		},
		{
			name:           "conditioned steps inside a stage",
			body:           "pipelines:\n  pull-requests:\n    '**':\n      - stage:\n          name: build\n          steps:\n            - step:\n                condition:\n                  changesets:\n                    includePaths: [web/**]\n                script: [make web]\n",
			wantPR:         true,
			wantPathFilter: true,
		},
		{
			name:           "conditioned steps inside a parallel block",
			body:           "pipelines:\n  pull-requests:\n    '**':\n      - parallel:\n          - step:\n              condition:\n                changesets:\n                  includePaths: [web/**]\n              script: [make web]\n          - step:\n              condition:\n                changesets:\n                  includePaths: [api/**]\n              script: [make api]\n",
			wantPR:         true,
			wantPathFilter: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scan := ScanPullRequestCI(writePipelines(t, tc.body))
			if scan.HasPRWorkflows != tc.wantPR {
				t.Errorf("HasPRWorkflows = %v, want %v", scan.HasPRWorkflows, tc.wantPR)
			}
			if scan.AllPathFiltered != tc.wantPathFilter {
				t.Errorf("AllPathFiltered = %v, want %v", scan.AllPathFiltered, tc.wantPathFilter)
			}
		})
	}
}

// TestBitbucketPipelinesCoverEveryBase records why the gate never waives on a
// Bitbucket repo that has pipelines: Bitbucket filters on a pull request's source
// branch and has no destination filter, so no base branch can prove a pipeline
// would have skipped it.
func TestBitbucketPipelinesCoverEveryBase(t *testing.T) {
	scan := ScanPullRequestCI(writePipelines(t,
		"pipelines:\n  pull-requests:\n    'feature/*':\n      - step:\n          script: [make test]\n"))
	for _, base := range []string{"main", "epic/COD-1-thing", "release/2026-08"} {
		if !scan.CoversBranch(base) {
			t.Errorf("CoversBranch(%q) = false, want every base covered", base)
		}
	}
}

// TestScanUnionsBothCIConfigurations proves the scan is keyed on the files a repo
// has rather than on its forge, so a repo carrying both is covered by either.
func TestScanUnionsBothCIConfigurations(t *testing.T) {
	dir := writePipelines(t, "pipelines:\n  custom:\n    release:\n      - step:\n          script: [make dist]\n")
	if scan := ScanPullRequestCI(dir); scan.HasPRWorkflows {
		t.Fatalf("HasPRWorkflows = true with only a custom pipeline, want false")
	}
	wf := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wf, "ci.yml"), []byte("on: pull_request\njobs: {}\n"), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if scan := ScanPullRequestCI(dir); !scan.HasPRWorkflows {
		t.Errorf("HasPRWorkflows = false once an Actions workflow exists, want true")
	}
}

func TestScanBitbucketPipelinesIgnoresAMissingFile(t *testing.T) {
	if scan := ScanPullRequestCI(t.TempDir()); scan.HasPRWorkflows || scan.AllPathFiltered {
		t.Errorf("scan = %+v, want an empty scan for a repo with no CI configuration", scan)
	}
}
