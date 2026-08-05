package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/event"
)

// TestExtractTestTargets pins the extraction over rubric text in all
// three forms the rubric names a test in — an explicit `go test … -run X` command,
// a named test file, a named test function — and pins the vacuous case: prose that
// names no test yields no targets, so the gate can never block on a fuzzy parse.
func TestExtractTestTargets(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []testTarget
	}{
		{
			name: "command form carries its packages and run pattern",
			text: "Run `go test ./internal/pipeline -run TestTestGate` and it must pass.",
			want: []testTarget{{
				Kind: targetCommand,
				Raw:  "go test ./internal/pipeline -run TestTestGate",
				Pkgs: []string{"./internal/pipeline"},
				Run:  "TestTestGate",
			}},
		},
		{
			name: "command form reads -run= and quoted patterns",
			text: `go test -race -count=1 ./internal/hub/... -run="TestSteerNotes"`,
			want: []testTarget{{
				Kind: targetCommand,
				Raw:  `go test -race -count=1 ./internal/hub/... -run="TestSteerNotes"`,
				Pkgs: []string{"./internal/hub/..."},
				Run:  "TestSteerNotes",
			}},
		},
		{
			name: "file form, bare and with a path",
			text: "internal/hub/steernotes_test.go must exist, and so must steps.test.ts",
			want: []testTarget{
				{Kind: targetFile, Raw: "internal/hub/steernotes_test.go", File: "internal/hub/steernotes_test.go"},
				{Kind: targetFile, Raw: "steps.test.ts", File: "steps.test.ts"},
			},
		},
		{
			name: "function form",
			text: "TestPhasePreamble covers the config preamble override.",
			want: []testTarget{{Kind: targetFunction, Raw: "TestPhasePreamble", Run: "TestPhasePreamble"}},
		},
		{
			name: "a command absorbs the file and function names inside it",
			text: "go test ./internal/pipeline/testgate_test.go -run TestTestGate",
			want: []testTarget{{
				Kind: targetCommand,
				Raw:  "go test ./internal/pipeline/testgate_test.go -run TestTestGate",
				Pkgs: []string{"./internal/pipeline/testgate_test.go"},
				Run:  "TestTestGate",
			}},
		},
		{
			name: "prose naming no test extracts nothing",
			text: "The wizard must not lose the operator's edits, and the docs should say so.",
			want: nil,
		},
		{
			name: "empty rubric extracts nothing",
			text: "",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractTestTargets(tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("extracted %d target(s) %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for i := range tc.want {
				w, g := tc.want[i], got[i]
				if g.Kind != w.Kind || g.Run != w.Run || g.File != w.File {
					t.Errorf("target %d = %+v, want %+v", i, g, w)
				}
				if strings.TrimSpace(g.Raw) != strings.TrimSpace(w.Raw) {
					t.Errorf("target %d raw = %q, want %q", i, g.Raw, w.Raw)
				}
				if strings.Join(g.Pkgs, ",") != strings.Join(w.Pkgs, ",") {
					t.Errorf("target %d pkgs = %v, want %v", i, g.Pkgs, w.Pkgs)
				}
			}
		})
	}
}

// TestGateTargets pins which rubric fields the gate reads, and how far it trusts
// each: required_tests names tests in every form, the acceptance criteria are
// trusted only for the unambiguous forms — a bare TestX in prose is as likely to be
// a symbol the slice introduces — and non_goals/fail_conditions are not read at
// all, since they name tests that must NOT exist or must fail.
func TestGateTargets(t *testing.T) {
	got := gateTargets(rubric{
		RequiredTests: []string{"TestWanted", "wanted_test.go"},
		AcceptanceCriteria: []string{
			"steps.test.ts covers the mapping",
			"The TestGate activity groups into the Verify step",
		},
		NonGoals:       []string{"TestForbidden"},
		FailConditions: []string{"TestAlsoForbidden fails"},
	})
	var names []string
	for _, tg := range got {
		names = append(names, string(tg.Kind)+":"+tg.Raw)
	}
	want := []string{"file:wanted_test.go", "function:TestWanted", "file:steps.test.ts"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("gateTargets = %v, want %v", names, want)
	}
}

// TestTestGatePassesVacuously: a run whose rubric names no test
// target, and whose diff cannot be listed, reaches verify with the gate reporting a
// pass and blocking nothing.
func TestTestGatePassesVacuously(t *testing.T) {
	id := "COD-1497001"
	repo := t.TempDir()
	writeGateRubric(t, id, rubric{
		Ticket:             id,
		AcceptanceCriteria: []string{"The wizard keeps the operator's edits."},
	})
	var buf bytes.Buffer
	p := newTestPipeline(t, &countingRunner{results: []error{nil}}, &fakeTracker{})
	p.RepoRoot = repo
	p.Events = event.New(&buf)

	if v, gated := p.testGate(context.Background(), id); gated {
		t.Fatalf("gate blocked a run with no test targets: %+v", v)
	}
	evs := kindEvents(t, &buf, event.KindTestGate)
	if len(evs) != 1 {
		t.Fatalf("emitted %d test_gate events, want 1", len(evs))
	}
	if got := strField(evs[0].Fields, "result"); got != "pass" {
		t.Errorf("event result = %q, want pass", got)
	}
	if evs[0].Phase != "testgate" {
		t.Errorf("event phase = %q, want testgate", evs[0].Phase)
	}
}

// TestTestGateNamesMissingRequiredTests: a rubric that names a test file and a
// test function neither of which exists blocks the run
// before verify, and names the exact missing targets in the failure lines the
// repair agent reads.
func TestTestGateNamesMissingRequiredTests(t *testing.T) {
	id := "COD-1497002"
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "present_test.go"), "package here\n\nimport \"testing\"\n\nfunc TestPresent(t *testing.T) {}\n")
	writeGateRubric(t, id, rubric{
		Ticket:             id,
		AcceptanceCriteria: []string{"The steer note queue survives a restart."},
		RequiredTests: []string{
			"steernotes_test.go exists",
			"TestPhasePreamble covers the override",
			"TestPresent still passes",
		},
	})
	var buf bytes.Buffer
	p := newTestPipeline(t, &countingRunner{results: []error{nil}}, &fakeTracker{})
	p.RepoRoot = repo
	p.Events = event.New(&buf)

	v, gated := p.testGate(context.Background(), id)
	if !gated {
		t.Fatal("gate passed a rubric naming two tests that do not exist")
	}
	if v.Pass {
		t.Error("gate verdict reports a pass")
	}
	fails := v.failureLines()
	for _, want := range []string{"steernotes_test.go", "TestPhasePreamble"} {
		if !strings.Contains(fails, want) {
			t.Errorf("failure lines %q do not name the missing target %s", fails, want)
		}
	}
	if strings.Contains(fails, "TestPresent") {
		t.Errorf("failure lines %q blame a test that does exist", fails)
	}
	evs := kindEvents(t, &buf, event.KindTestGate)
	if len(evs) != 1 || strField(evs[0].Fields, "result") != "fail" {
		t.Fatalf("test_gate events = %+v, want one failing event", evs)
	}
}

// TestTestGateFindsATestNamedWithoutItsPath covers the way a rubric usually names a
// test: a bare file name with no directory, and a function with no package.
func TestTestGateFindsATestNamedWithoutItsPath(t *testing.T) {
	id := "COD-1497003"
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "internal", "hub", "steernotes_test.go"),
		"package hub\n\nimport \"testing\"\n\nfunc TestSteerNoteQueue(t *testing.T) {}\n")
	writeGateRubric(t, id, rubric{
		Ticket:        id,
		RequiredTests: []string{"steernotes_test.go", "TestSteerNoteQueue"},
	})
	p := newTestPipeline(t, &countingRunner{results: []error{nil}}, &fakeTracker{})
	p.RepoRoot = repo

	if v, gated := p.testGate(context.Background(), id); gated {
		t.Fatalf("gate blocked on tests that exist: %s", v.failureLines())
	}
}

// TestChangedTestScope pins which changed files are re-run: Go test packages
// and web test files that are still on disk, and nothing else.
func TestChangedTestScope(t *testing.T) {
	repo := t.TempDir()
	for _, rel := range []string{
		"internal/pipeline/testgate_test.go",
		"internal/pipeline/testgate.go",
		"internal/hub/hub_test.go",
		"web/src/lib/steps.test.ts",
		"web/src/lib/steps.ts",
	} {
		mustWrite(t, filepath.Join(repo, filepath.FromSlash(rel)), "x")
	}
	changed := []string{
		"internal/pipeline/testgate_test.go",
		"internal/pipeline/testgate.go",
		"internal/hub/hub_test.go",
		"web/src/lib/steps.test.ts",
		"web/src/lib/steps.ts",
		"internal/gone/deleted_test.go", // removed by the slice — nothing left to run
	}
	goDirs, web := changedTestScope(repo, changed)
	if want := []string{"internal/hub", "internal/pipeline"}; strings.Join(goDirs, ",") != strings.Join(want, ",") {
		t.Errorf("go dirs = %v, want %v", goDirs, want)
	}
	if want := []string{"web/src/lib/steps.test.ts"}; strings.Join(web, ",") != strings.Join(want, ",") {
		t.Errorf("web files = %v, want %v", web, want)
	}
}

// TestTestFailureOutput separates a real failure — which the gate blames on the
// slice — from a toolchain that would not run at all, which it must not.
func TestTestFailureOutput(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"failing assertion", "--- FAIL: TestThing (0.00s)\nFAIL\n", true},
		{"data race", "WARNING: DATA RACE\nRead at 0x00c000...\n", true},
		{"panic", "panic: runtime error: index out of range\n", true},
		{"build failure", "# example/pkg [build failed]\nFAIL\texample/pkg [build failed]\n", true},
		{"race unsupported", "go: -race is only supported on linux/amd64, ...\n", false},
		{"no toolchain", "exec: \"go\": executable file not found in $PATH\n", false},
		{"clean", "ok  \texample/pkg\t0.4s\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := testFailureOutput(tc.out); got != tc.want {
				t.Errorf("testFailureOutput(%q) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}

// TestRepeatFlag pins the web half: repeats are used only when the
// runner's own help advertises them, so an unknown flag can never fail a run for a
// reason that has nothing to do with the slice.
func TestRepeatFlag(t *testing.T) {
	if got := repeatFlag("  --repeat <times>  Run each test file N times\n"); strings.Join(got, "") != "--repeat=3" {
		t.Errorf("repeatFlag with a repeat option = %v, want --repeat=3", got)
	}
	// Vitest's help mentions --project "can be repeated" but offers no repeat flag.
	if got := repeatFlag("  --retry <times>  Retry failing tests\n  --project <name>  This can be repeated\n"); got != nil {
		t.Errorf("repeatFlag with no repeat option = %v, want nil", got)
	}
}

// TestTestGateFailsARacyChangedTest: a package whose test files this run touched,
// and whose tests do not survive -race -count=3, blocks the
// run before verify and carries the toolchain's own output into the failure lines.
func TestTestGateFailsARacyChangedTest(t *testing.T) {
	requireRaceToolchain(t)
	id := "COD-1497004"
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "go.mod"), "module example.com/gate\n\ngo 1.25\n")
	mustWrite(t, filepath.Join(repo, "flaky", "flaky_test.go"), `package flaky

import (
	"sync"
	"testing"
)

var runs int

func TestOnlyPassesOnce(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runs++
		}()
	}
	wg.Wait()
}
`)
	writeGateRubric(t, id, rubric{Ticket: id, AcceptanceCriteria: []string{"the flaky package holds up"}})
	p := newTestPipeline(t, &countingRunner{results: []error{nil}}, &fakeTracker{})
	p.RepoRoot = repo
	p.Git = filesGit{files: []string{"flaky/flaky_test.go"}}

	v, gated := p.testGate(context.Background(), id)
	if !gated {
		t.Fatal("gate passed a changed test that does not survive -race")
	}
	fails := v.failureLines()
	if !strings.Contains(fails, "flaky") {
		t.Errorf("failure lines %q do not name the package", fails)
	}
	if !strings.Contains(fails, "DATA RACE") {
		t.Errorf("failure lines %q carry none of the race detector's output", fails)
	}
}

// TestTestGateSkipsWithoutARepoRoot keeps the gate off the runs it cannot measure.
func TestTestGateSkipsWithoutARepoRoot(t *testing.T) {
	p := newTestPipeline(t, &countingRunner{results: []error{nil}}, &fakeTracker{})
	if _, gated := p.testGate(context.Background(), "COD-1497005"); gated {
		t.Error("gate blocked a run with no repo root to measure")
	}
}

// gateRunner records the label and prompt of every agent call, and writes a passing
// verdict whenever a verify attempt runs — standing in for the whole verify/repair
// agent chain so the routing can be observed without one.
type gateRunner struct {
	mu      sync.Mutex
	path    string
	labels  []string
	prompts []string
}

func (r *gateRunner) Run(_ context.Context, prompt, label string) (agent.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.labels = append(r.labels, label)
	r.prompts = append(r.prompts, prompt)
	if strings.HasPrefix(label, "verify") {
		data, _ := json.Marshal(verdict{Pass: true, Summary: "all good"})
		_ = os.WriteFile(r.path, data, 0o644)
	}
	return agent.Result{Final: "ok"}, nil
}

func (r *gateRunner) Provider() string { return "claude" }

// TestVerifyRoutesGateFailureStraightToRepair: a failing gate spends no verify
// turn — the first agent call is the
// repair, handed the gate's own output — and the run then verifies normally.
func TestVerifyRoutesGateFailureStraightToRepair(t *testing.T) {
	id := "COD-1497006"
	writeHandoff(t, id)
	writeGateRubric(t, id, rubric{
		Ticket:             id,
		AcceptanceCriteria: []string{"the queue survives a restart"},
		RequiredTests:      []string{"steernotes_test.go"},
	})
	runner := &gateRunner{path: verifyPath(id)}
	p := newTestPipeline(t, runner, &fakeTracker{})
	p.RepoRoot = t.TempDir()
	p.MaxRepairs = 1

	if err := p.Verify(context.Background(), id); err != nil {
		t.Fatalf("Verify err = %v, want nil (repaired, then verified)", err)
	}
	if want := []string{"repair1", "verify-retry1"}; strings.Join(runner.labels, ",") != strings.Join(want, ",") {
		t.Fatalf("agent calls = %v, want %v (no verify turn in front of the repair)", runner.labels, want)
	}
	if !strings.Contains(runner.prompts[0], "steernotes_test.go") {
		t.Errorf("the repair prompt does not carry the gate's failure: %q", runner.prompts[0])
	}
}

// TestVerifyWithoutAGateFailureStartsAtVerify: a clean gate leaves the loop
// exactly as it was, opening on the verify turn.
func TestVerifyWithoutAGateFailureStartsAtVerify(t *testing.T) {
	id := "COD-1497007"
	writeHandoff(t, id)
	writeGateRubric(t, id, rubric{
		Ticket:             id,
		AcceptanceCriteria: []string{"the queue survives a restart"},
	})
	runner := &gateRunner{path: verifyPath(id)}
	p := newTestPipeline(t, runner, &fakeTracker{})
	p.RepoRoot = t.TempDir()
	p.MaxRepairs = 1

	if err := p.Verify(context.Background(), id); err != nil {
		t.Fatalf("Verify err = %v, want nil", err)
	}
	if want := []string{"verify"}; strings.Join(runner.labels, ",") != strings.Join(want, ",") {
		t.Fatalf("agent calls = %v, want %v", runner.labels, want)
	}
}

func writeGateRubric(t *testing.T, id string, r rubric) {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rubricPath(id), data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(rubricPath(id)) })
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// requireRaceToolchain skips a test that needs a real race-detector run where the
// host cannot give it one.
func requireRaceToolchain(t *testing.T) {
	t.Helper()
	if !hasGoToolchain() {
		t.Skip("no go toolchain on PATH")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the race detector needs a C toolchain this host may not have")
	}
	out, err := exec.Command("go", "env", "CGO_ENABLED").Output()
	if err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Skip("cgo is off, so -race cannot run")
	}
}
