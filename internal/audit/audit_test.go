package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// readCalls names the standard-library file-read entry points the audit tracks.
// os.Stat and friends (metadata, not content) are deliberately excluded, as are
// writes and embed.FS reads (which never touch a selector named os/ioutil/filepath).
var readCalls = map[string]map[string]bool{
	"os":       {"Open": true, "ReadFile": true, "ReadDir": true},
	"ioutil":   {"ReadFile": true, "ReadDir": true},
	"filepath": {"Walk": true, "WalkDir": true, "Glob": true},
}

// allowedReaders maps each source file that reads the disk at runtime to its ADR
// 0008 §6 exemption. The invariant: this set is the whole exemption list. A new
// runtime disk read must either go through the hub instead, or — if it is
// genuinely one of the exempt kinds — be added here with its category. A bare new
// entry with no exemption is the smell the review should catch.
var allowedReaders = map[string]string{
	// §1 Configuration — read on every run; not run data.
	"internal/config/config.go":    "config files",
	"internal/config/ci_detect.go": "repo CI-workflow detection",

	// §2 Repo-owned content — owned by the target repo; trau only reads it.
	"internal/agent/skills.go":          "repo skills / skills-lock.json / package.json",
	"internal/agent/skillmeta.go":       "repo skills' SKILL.md manifests",
	"internal/checks/checks.go":         "repo .trau/checks",
	"internal/skillrules/skillrules.go": "repo .trau/skills-rules.json",
	"internal/state/gitignore.go":       "target repo .gitignore",

	// §3 Provider-owned files — owned by the provider CLIs; read for usage/stats.
	"internal/agent/agent.go":        "provider session files + agent .result.json read-back",
	"internal/agent/codexsession.go": "codex session rollouts",
	"internal/agent/kimisession.go":  "kimi session rollouts",
	"internal/agent/transcript.go":   "provider session transcripts",
	"internal/usage/probe/claude.go": "provider usage window",
	"internal/usage/probe/codex.go":  "provider usage window",

	// §4 Agent-interface files — the ephemeral child↔agent-CLI /tmp wire.
	"internal/pipeline/pipeline.go":   "/tmp handoff/verdict + git-tracked working-tree files",
	"internal/pipeline/rubric.go":     "/tmp rubric payload",
	"internal/pipeline/buildnotes.go": "/tmp build-notes payload",
	"internal/pipeline/lessons.go":    "/tmp lesson-distill payload",
	"internal/pipeline/timelog.go":    "/tmp timelog-estimate payload",
	"internal/pipeline/qacapture.go":  "/tmp QA-credential capture payload",
	"internal/tracker/internal.go":    "/tmp verdict payload",
	"internal/tracker/jira.go":        "/tmp verdict payload",

	// §5 Timelog — out of scope for this epic.
	"internal/timelog/timelog.go": "timelog under ~/.trau/time",

	// Hub-owned content store — the hub reads its own content-addressed
	// attachment blobs (under <hub home>/attachments) to serve their bytes; this
	// is the hub, not run data routed around it.
	"internal/hubstore/attachmentblobs.go": "hub attachment blob store",

	// One-shot legacy importers — the only file-era code, gated to first hub touch.
	"internal/state/state.go":             "file-era checkpoint store read by the checkpoint importer",
	"internal/hubstore/checkpoints.go":    "legacy checkpoint import",
	"internal/hubstore/artifacts.go":      "legacy artifact import",
	"internal/hubstore/lessons.go":        "legacy lessons import",
	"internal/hubstore/phaselogs.go":      "legacy phase-log import",
	"internal/hubstore/queue.go":          "legacy queue.json import",
	"internal/hubstore/registrations.go":  "legacy registration import",
	"internal/hubstore/legacy_rundata.go": "doctor leftover-run-data probe",
}

func TestNoRuntimeFileReadsOutsideExemptions(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var offenders []string

	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel := filepath.ToSlash(mustRel(t, root, path))
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", rel, err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := readCallSelector(n)
				if !ok {
					return true
				}
				if _, allowed := allowedReaders[rel]; allowed {
					return true
				}
				offenders = append(offenders, rel+": "+sel+" at "+fset.Position(n.Pos()).String())
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("runtime file reads outside the ADR 0008 §6 exemption list:\n  %s\n\n"+
			"Route run data through the hub, or if this read is genuinely exempt "+
			"(config, repo-owned, provider-owned, /tmp agent-interface, timelog, or a "+
			"one-shot legacy importer) add the file to allowedReaders with its category.",
			strings.Join(offenders, "\n  "))
	}
}

// readCallSelector reports the pkg.Fn text of a tracked file-read call, or ok=false.
func readCallSelector(n ast.Node) (string, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	if fns := readCalls[pkg.Name]; fns[sel.Sel.Name] {
		return pkg.Name + "." + sel.Sel.Name, true
	}
	return "", false
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from the test directory")
		}
		dir = parent
	}
}

func mustRel(t *testing.T, base, path string) string {
	t.Helper()
	rel, err := filepath.Rel(base, path)
	if err != nil {
		t.Fatalf("rel %s: %v", path, err)
	}
	return rel
}
