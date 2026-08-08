package pipeline

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/activity"
	"github.com/RomkaLTU/trau/internal/event"
)

// The mechanical test gate is the cheap deterministic step between build and
// verify. It checks two things a machine can name for free — a test the rubric
// required that was never written, and a new test that only passes when the race
// detector is off — spawns no agent, and routes a failure straight to repair with
// its own output as the failure lines.
//
// Everything it cannot measure reads as a pass: a rubric that names no tests, a
// repo whose diff cannot be listed, a missing toolchain, an exhausted budget.
// Blocking delivery on the gate's own instrumentation would cost more than the
// verify turn it is trying to save.
const (
	testGateBudget     = 12 * time.Minute
	testGatePkgBudget  = 6 * time.Minute
	testGateListBudget = 90 * time.Second
	testGateHelpBudget = 30 * time.Second

	// testGateRaceCount is the -count a changed package's tests must survive under
	// -race before the run is allowed to spend a verify turn.
	testGateRaceCount = 3

	// testGateOutputLines is how much of a failing command's output travels into the
	// failure lines the repair agent reads, from each end.
	testGateOutputLines = 12
)

// testGate runs the mechanical checks and returns the verdict a failure should
// feed the repair loop, blocked true only when the gate actually blocked. The
// verdict is shaped exactly like a verify verdict so every downstream consumer
// takes it unchanged.
func (p *Pipeline) testGate(ctx context.Context, id string) (verdict, bool) {
	if p.workRoot() == "" {
		return verdict{}, false
	}
	p.setActivity(id, activity.TestGate, "")

	gctx, cancel := context.WithTimeout(ctx, testGateBudget)
	defer cancel()

	targets := gateTargets(p.gateRubric(id))
	fails := p.missingTargets(gctx, targets)
	changed, raceFails := p.unstableChangedTests(gctx)
	fails = append(fails, raceFails...)

	if gctx.Err() != nil {
		p.logf("  ⚠ test gate: budget exhausted before it could finish — continuing to verify")
		return verdict{}, false
	}

	scope := fmt.Sprintf("%d required test target(s), %d changed test target(s)", len(targets), changed)
	if len(fails) == 0 {
		p.logf("  ✓ test gate passed (%s)", scope)
		p.emitTestGate(id, "pass", "test gate passed — "+scope, len(targets), changed, 0)
		return verdict{}, false
	}

	p.logf("  ⚠ test gate failed (%s) — routing to repair without spending a verify turn", scope)
	for _, f := range fails {
		p.logf("  ↳ %s", firstLine(f))
	}
	p.emitTestGate(id, "fail", "test gate failed — "+firstLine(fails[0]), len(targets), changed, len(fails))
	return verdict{
		Pass:     false,
		Summary:  "mechanical test gate failed before verify: " + scope + " checked, " + fmt.Sprintf("%d problem(s)", len(fails)),
		Failures: fails,
	}, true
}

// emitTestGate records the gate's outcome so the run view can show why a run
// reached repair with no verify turn in front of it.
func (p *Pipeline) emitTestGate(id, result, msg string, targets, changed, failures int) {
	if p.Events == nil {
		return
	}
	p.Events.Emit(event.KindTestGate, string(activity.TestGate), msg, map[string]any{
		"ticket":   id,
		"result":   result,
		"targets":  targets,
		"changed":  changed,
		"failures": failures,
	})
}

// gateRubric reads the rubric the handoff wrote for id, restoring the durable copy
// first, or the zero rubric when there is none.
func (p *Pipeline) gateRubric(id string) rubric {
	p.restoreRubric(id)
	r, _ := readRubric(rubricPath(id))
	return r
}

// gateTargets is the set of required tests a rubric names. All three forms are read
// from required_tests; the acceptance criteria are scanned only for the unambiguous
// forms, since a bare TestX in prose is as likely to name a symbol the slice
// introduces as a test it must write. non_goals and fail_conditions are not read at
// all — they name tests that must NOT exist, so demanding them would invert the
// check.
func gateTargets(r rubric) []testTarget {
	targets := extractTestTargets(strings.Join(r.RequiredTests, "\n"))
	seen := map[string]bool{}
	for _, t := range targets {
		seen[targetKey(t)] = true
	}
	for _, t := range extractTestTargets(strings.Join(r.AcceptanceCriteria, "\n")) {
		if t.Kind == targetFunction || seen[targetKey(t)] {
			continue
		}
		seen[targetKey(t)] = true
		targets = append(targets, t)
	}
	return targets
}

// targetKind is the shape a required test was named in.
type targetKind string

const (
	targetCommand  targetKind = "command"
	targetFile     targetKind = "file"
	targetFunction targetKind = "function"
)

// testTarget is one test the rubric names. Raw is the text it was extracted from,
// quoted back verbatim when the target is missing so the repair agent gets the
// exact string it has to satisfy.
type testTarget struct {
	Kind targetKind
	Raw  string
	Pkgs []string // command form: the package patterns it names
	Run  string   // command form: its -run pattern; function form: the name
	File string   // file form: the path it names
}

var (
	// A go test command runs to the end of its line, or to the closing backtick of
	// the markdown span a rubric usually writes it in. Quotes stay inside it so a
	// quoted -run pattern survives.
	reGoTestCmd   = regexp.MustCompile("\\bgo\\s+test\\b[^\\n\\r`]*")
	reGoTestFile  = regexp.MustCompile(`[\w./\\-]*\w_test\.go\b`)
	reWebTestFile = regexp.MustCompile(`[\w./\\-]*\w\.(?:test|spec)\.[jt]sx?\b`)
	reTestFunc    = regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9_]*\b`)
	// A package pattern safe to hand to the toolchain as argv.
	rePkgPattern = regexp.MustCompile(`^[\w./@-]+$`)
)

// extractTestTargets pulls the test targets named in text, in the three forms the
// rubric writes them: an explicit `go test … -run X` command, a named test file,
// and a named test function. A command absorbs the file and function names inside
// it — the span is blanked before the narrower forms are scanned — so one named
// command yields one target rather than three overlapping ones. Text that names
// nothing recognizable yields no targets, so the gate never blocks on a fuzzy parse.
func extractTestTargets(text string) []testTarget {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var targets []testTarget
	seen := map[string]bool{}
	add := func(t testTarget) {
		if seen[targetKey(t)] {
			return
		}
		seen[targetKey(t)] = true
		targets = append(targets, t)
	}

	rest := text
	for _, span := range reGoTestCmd.FindAllString(text, -1) {
		add(parseGoTestCmd(strings.TrimSpace(span)))
		rest = blankOut(rest, span)
	}
	for _, re := range []*regexp.Regexp{reGoTestFile, reWebTestFile} {
		for _, f := range re.FindAllString(rest, -1) {
			file := strings.TrimPrefix(strings.TrimSpace(f), "./")
			add(testTarget{Kind: targetFile, Raw: file, File: file})
			rest = blankOut(rest, f)
		}
	}
	for _, fn := range reTestFunc.FindAllString(rest, -1) {
		add(testTarget{Kind: targetFunction, Raw: fn, Run: fn})
	}
	return targets
}

// targetKey identifies a target for de-duplication: the same test named twice, in
// the same form, is one requirement.
func targetKey(t testTarget) string {
	return string(t.Kind) + "\x00" + t.File + "\x00" + t.Run + "\x00" + t.Raw
}

// parseGoTestCmd reads the package patterns and the -run pattern out of one
// `go test` command. Anything it cannot classify is ignored rather than guessed.
func parseGoTestCmd(cmd string) testTarget {
	t := testTarget{Kind: targetCommand, Raw: cmd}
	fields := strings.Fields(cmd)
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		switch {
		case f == "-run" && i+1 < len(fields):
			t.Run = unquoteArg(fields[i+1])
			i++
		case strings.HasPrefix(f, "-run="):
			t.Run = unquoteArg(strings.TrimPrefix(f, "-run="))
		case strings.HasPrefix(f, "-"), f == "go", f == "test":
		default:
			if pkg := unquoteArg(f); rePkgPattern.MatchString(pkg) && (strings.HasPrefix(pkg, ".") || strings.Contains(pkg, "/")) {
				t.Pkgs = append(t.Pkgs, pkg)
			}
		}
	}
	return t
}

func unquoteArg(s string) string {
	return strings.Trim(s, `'"`)
}

// blankOut replaces the first occurrence of span with spaces, keeping every other
// offset in text where it was so the remaining regexes see the same layout.
func blankOut(text, span string) string {
	i := strings.Index(text, span)
	if i < 0 {
		return text
	}
	return text[:i] + strings.Repeat(" ", len(span)) + text[i+len(span):]
}

// missingTargets names every required test that is not there, answering from the
// repo's own source wherever it can and reaching for `go test -list` only to
// evaluate a command's -run pattern.
func (p *Pipeline) missingTargets(ctx context.Context, targets []testTarget) []string {
	if len(targets) == 0 {
		return nil
	}
	idx := newTestIndex(p.workRoot())
	var fails []string
	for _, t := range targets {
		if ctx.Err() != nil {
			return fails
		}
		switch t.Kind {
		case targetFile:
			if !idx.hasFile(t.File) {
				fails = append(fails, fmt.Sprintf("required test file %q does not exist in the repo — the rubric names it, so this slice has to write it", t.File))
			}
		case targetFunction:
			if len(idx.dirsFor(t.Run)) == 0 {
				fails = append(fails, fmt.Sprintf("required test function %s() does not exist in the repo — the rubric names it, so this slice has to write it", t.Run))
			}
		case targetCommand:
			if msg := p.commandTargetMiss(ctx, idx, t); msg != "" {
				fails = append(fails, msg)
			}
		}
	}
	return fails
}

// commandTargetMiss reports why a named `go test` command matches no test, or ""
// when it matches at least one. A toolchain that will not answer is not a miss: the
// check falls back to the source index.
func (p *Pipeline) commandTargetMiss(ctx context.Context, idx *testIndex, t testTarget) string {
	if n, ok := p.listTests(ctx, t); ok {
		if n > 0 {
			return ""
		}
		return fmt.Sprintf("required test command %q lists no tests — the rubric names it, so this slice has to write the test it runs", t.Raw)
	}
	if t.Run == "" {
		return ""
	}
	if len(idx.matching(t.Run)) > 0 {
		return ""
	}
	return fmt.Sprintf("required test command %q matches no test in the repo — no test name matches %s", t.Raw, t.Run)
}

// listTests counts the tests `go test -list` reports for a command's packages. ok
// is false when the listing could not be trusted — no toolchain, no explicit
// package to list (listing ./... would compile the world), or a package that does
// not build — so the caller can fall back instead of blocking on it.
func (p *Pipeline) listTests(ctx context.Context, t testTarget) (int, bool) {
	if len(t.Pkgs) == 0 || !hasGoToolchain() {
		return 0, false
	}
	pattern := t.Run
	if pattern == "" {
		pattern = "."
	}
	lctx, cancel := context.WithTimeout(ctx, testGateListBudget)
	defer cancel()
	out, err := p.runGateCmd(lctx, p.workRoot(), "go", append([]string{"test", "-list", pattern}, t.Pkgs...)...)
	if err != nil {
		p.logf("  ⚠ test gate: could not list tests for %q (not treating it as missing): %s", t.Raw, firstLine(string(out)))
		return 0, false
	}
	return countListedTests(string(out)), true
}

// countListedTests counts the test names in `go test -list` output, which
// interleaves them with the per-package "ok …" summary lines.
func countListedTests(out string) int {
	n := 0
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		switch {
		case ln == "", strings.HasPrefix(ln, "ok "), strings.HasPrefix(ln, "?"), strings.HasPrefix(ln, "FAIL"), strings.HasPrefix(ln, "---"):
			continue
		}
		if reTestFunc.MatchString(ln) || strings.HasPrefix(ln, "Fuzz") || strings.HasPrefix(ln, "Example") || strings.HasPrefix(ln, "Benchmark") {
			n++
		}
	}
	return n
}

// unstableChangedTests requires every package whose test files this run touched to
// survive -race -count=3, and every changed web test file to pass its own runner.
// measured counts what it actually re-ran.
func (p *Pipeline) unstableChangedTests(ctx context.Context) (measured int, fails []string) {
	changed, ok := p.sliceChangedFiles(ctx)
	if !ok {
		return 0, nil
	}
	goDirs, web := changedTestScope(p.workRoot(), changed)
	for _, dir := range goDirs {
		if ctx.Err() != nil {
			return measured, fails
		}
		if f := p.raceCheckGo(ctx, dir); f != "" {
			fails = append(fails, f)
		}
		measured++
	}
	groups := groupWebTests(p.workRoot(), web)
	for _, ws := range slices.Sorted(maps.Keys(groups)) {
		if ctx.Err() != nil {
			return measured, fails
		}
		if f := p.repeatCheckWeb(ctx, ws, groups[ws]); f != "" {
			fails = append(fails, f)
		}
		measured += len(groups[ws])
	}
	return measured, fails
}

// changedTestScope splits a run's changed files into the Go package directories and
// the web test files whose stability the gate has to re-establish. Paths that no
// longer exist are dropped.
func changedTestScope(root string, changed []string) (goDirs, webFiles []string) {
	seenDir := map[string]bool{}
	for _, raw := range changed {
		rel := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(raw)), "./")
		if rel == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			continue
		}
		switch {
		case strings.HasSuffix(rel, "_test.go"):
			dir := path.Dir(rel)
			if !seenDir[dir] {
				seenDir[dir] = true
				goDirs = append(goDirs, dir)
			}
		case isWebTestFile(rel):
			webFiles = append(webFiles, rel)
		}
	}
	sort.Strings(goDirs)
	sort.Strings(webFiles)
	return goDirs, webFiles
}

func isWebTestFile(rel string) bool {
	base := path.Base(rel)
	for _, mid := range []string{".test.", ".spec."} {
		if i := strings.Index(base, mid); i > 0 {
			switch path.Ext(base) {
			case ".ts", ".tsx", ".js", ".jsx":
				return true
			}
		}
	}
	return false
}

// raceCheckGo runs one package's tests under the race detector, returning the
// failure lines when they do not hold up. The package is run from its own directory
// so a workspace with its own module is measured on its own terms. A command that
// could not run at all is not a failure.
func (p *Pipeline) raceCheckGo(ctx context.Context, dir string) string {
	if !hasGoToolchain() {
		return ""
	}
	pctx, cancel := context.WithTimeout(ctx, testGatePkgBudget)
	defer cancel()
	out, err := p.runGateCmd(pctx, filepath.Join(p.workRoot(), filepath.FromSlash(dir)),
		"go", "test", "-race", fmt.Sprintf("-count=%d", testGateRaceCount), ".")
	if err == nil {
		return ""
	}
	if pctx.Err() != nil {
		p.logf("  ⚠ test gate: ./%s did not finish under -race within the budget — leaving it to verify", dir)
		return ""
	}
	if !testFailureOutput(string(out)) {
		p.logf("  ⚠ test gate: could not run ./%s under -race (not treating it as a failure): %s", dir, firstLine(string(out)))
		return ""
	}
	return fmt.Sprintf("this run changed tests in ./%s and they do not survive `go test -race -count=%d .`:\n%s",
		dir, testGateRaceCount, gateOutputExcerpt(string(out)))
}

// testFailureOutput distinguishes a real test failure — a failing assertion, a
// data race, a panic, a test package that does not build — from the toolchain
// refusing to run at all, which the gate must never blame on the slice.
func testFailureOutput(out string) bool {
	for _, marker := range []string{"--- FAIL", "\nFAIL", "DATA RACE", "panic: "} {
		if strings.Contains(out, marker) {
			return true
		}
	}
	return strings.HasPrefix(out, "FAIL")
}

// groupWebTests maps each changed web test file onto the workspace that owns a
// runner. Files with no local runner above them are dropped: the gate never
// installs anything and never reaches the network.
func groupWebTests(root string, files []string) map[string][]string {
	out := map[string][]string{}
	for _, rel := range files {
		ws, ok := webRunnerDir(root, path.Dir(rel))
		if !ok {
			continue
		}
		wsRel, err := filepath.Rel(filepath.Join(root, filepath.FromSlash(ws)), filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		out[ws] = append(out[ws], filepath.ToSlash(wsRel))
	}
	return out
}

// webRunnerDir walks up from dir to the repo root looking for a workspace with
// vitest already installed locally.
func webRunnerDir(root, dir string) (string, bool) {
	for cur := dir; ; cur = path.Dir(cur) {
		if vitestBin(filepath.Join(root, filepath.FromSlash(cur))) != "" {
			return cur, true
		}
		if cur == "." || cur == "/" {
			return "", false
		}
	}
}

// vitestBin returns the workspace's own vitest binary, or "" when it has none.
func vitestBin(ws string) string {
	for _, name := range []string{"vitest", "vitest.cmd"} {
		bin := filepath.Join(ws, "node_modules", ".bin", name)
		if fi, err := os.Stat(bin); err == nil && !fi.IsDir() {
			return bin
		}
	}
	return ""
}

// repeatCheckWeb re-runs a workspace's changed test files, repeated where the
// runner offers repeats and once where it does not.
func (p *Pipeline) repeatCheckWeb(ctx context.Context, ws string, files []string) string {
	dir := filepath.Join(p.workRoot(), filepath.FromSlash(ws))
	bin := vitestBin(dir)
	if bin == "" {
		return ""
	}
	args := append([]string{"run"}, files...)
	args = append(args, p.vitestRepeatArgs(ctx, bin)...)
	pctx, cancel := context.WithTimeout(ctx, testGatePkgBudget)
	defer cancel()
	out, err := p.runGateCmd(pctx, dir, bin, args...)
	if err == nil {
		return ""
	}
	if pctx.Err() != nil {
		p.logf("  ⚠ test gate: %s tests did not finish within the budget — leaving them to verify", ws)
		return ""
	}
	return fmt.Sprintf("this run changed the tests %s in %s and they do not pass:\n%s",
		strings.Join(files, ", "), ws, gateOutputExcerpt(string(out)))
}

// vitestRepeatArgs asks the installed runner whether it can repeat a run, and
// returns the flag only when it says so — an unknown flag would fail the run for a
// reason that has nothing to do with the slice.
func (p *Pipeline) vitestRepeatArgs(ctx context.Context, bin string) []string {
	hctx, cancel := context.WithTimeout(ctx, testGateHelpBudget)
	defer cancel()
	out, err := exec.CommandContext(hctx, bin, "--help").CombinedOutput()
	if err != nil {
		return nil
	}
	return repeatFlag(string(out))
}

// repeatFlag reads a runner's help for a repeat option, so a runner that grows one
// starts being repeated without any change here.
func repeatFlag(help string) []string {
	for _, flag := range []string{"--repeats", "--repeat"} {
		if strings.Contains(help, flag) {
			return []string{fmt.Sprintf("%s=%d", flag, testGateRaceCount)}
		}
	}
	return nil
}

// runGateCmd runs one gate command with no shell in between: the package patterns
// and file names come from rubric text and from the diff, and they are passed as
// argv rather than interpolated into a command line.
func (p *Pipeline) runGateCmd(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	p.logf("  ↳ test-gate: %s %s", filepath.Base(name), strings.Join(args, " "))
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	return c.CombinedOutput()
}

func hasGoToolchain() bool {
	_, err := exec.LookPath("go")
	return err == nil
}

// gateOutputExcerpt keeps the head and tail of a failing run's output. Cutting only
// the tail would drop the race report header, which is the whole diagnosis.
func gateOutputExcerpt(out string) string {
	var lines []string
	for _, ln := range strings.Split(out, "\n") {
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, ln)
		}
	}
	if len(lines) <= 2*testGateOutputLines {
		return strings.Join(lines, "\n")
	}
	head := lines[:testGateOutputLines]
	tail := lines[len(lines)-testGateOutputLines:]
	elision := fmt.Sprintf("  … %d line(s) omitted …", len(lines)-2*testGateOutputLines)
	return strings.Join(append(append(append([]string{}, head...), elision), tail...), "\n")
}

func firstLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return strings.TrimSpace(s)
}

// testIndex is a one-pass inventory of a repo's test files — the cheap source of
// truth for "does this named test exist?", with no compilation involved.
type testIndex struct {
	root  string
	files []string            // repo-relative, slash-separated
	funcs map[string][]string // test function name → directories declaring it
}

// testIndexMaxFiles keeps a pathological tree from turning the gate into a walk.
const testIndexMaxFiles = 20000

// skipIndexDirs are the trees a repo's own tests never live in, and which would
// otherwise dominate the walk.
var skipIndexDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	"coverage":     true,
	"target":       true,
}

var reTestDecl = regexp.MustCompile(`(?m)^func\s+(Test[A-Za-z0-9_]*)\s*\(`)

func newTestIndex(root string) *testIndex {
	idx := &testIndex{root: root}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			name := d.Name()
			if skipIndexDirs[name] || (strings.HasPrefix(name, ".") && name != ".") {
				return fs.SkipDir
			}
			return nil
		}
		if len(idx.files) >= testIndexMaxFiles {
			return fs.SkipAll
		}
		if strings.HasSuffix(rel, "_test.go") || isWebTestFile(rel) {
			idx.files = append(idx.files, rel)
		}
		return nil
	})
	return idx
}

// hasFile reports whether the index holds the named test file. A path with
// directories matches as a suffix of a real path; a bare file name matches on its
// own, since a rubric routinely names one without saying where it goes.
func (idx *testIndex) hasFile(rel string) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(rel)), "./")
	if rel == "" {
		return false
	}
	bare := !strings.Contains(rel, "/")
	for _, f := range idx.files {
		if f == rel || strings.HasSuffix(f, "/"+rel) {
			return true
		}
		if bare && path.Base(f) == rel {
			return true
		}
	}
	return false
}

// dirsFor lists the package directories declaring a test function of that name.
func (idx *testIndex) dirsFor(name string) []string {
	idx.scanFuncs()
	return idx.funcs[name]
}

// matching lists the test functions a -run pattern selects, read the way the
// toolchain reads it: an unanchored regexp over the test name. A pattern that is
// not a valid regexp is compared literally rather than discarded.
func (idx *testIndex) matching(pattern string) []string {
	idx.scanFuncs()
	re, err := regexp.Compile(pattern)
	var out []string
	for name := range idx.funcs {
		if err != nil {
			if strings.Contains(name, pattern) {
				out = append(out, name)
			}
			continue
		}
		if re.MatchString(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (idx *testIndex) scanFuncs() {
	if idx.funcs != nil {
		return
	}
	idx.funcs = map[string][]string{}
	for _, f := range idx.files {
		if !strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(idx.root, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		dir := path.Dir(f)
		for _, m := range reTestDecl.FindAllSubmatch(data, -1) {
			name := string(m[1])
			if !slices.Contains(idx.funcs[name], dir) {
				idx.funcs[name] = append(idx.funcs[name], dir)
			}
		}
	}
}
