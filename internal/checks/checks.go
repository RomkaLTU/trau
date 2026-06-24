// Package checks is the pluggable verify-check library. A target repo declares
// named verification checks under .trau/checks (YAML); the cold verify phase runs
// every applicable check and folds the per-check results into its {pass, summary,
// failures} verdict. A check's severity gates the merge: an "error" check that
// fails blocks, a "warn" check that fails is surfaced but does not block.
//
// Checks are run by the verify *agent* (not by trau): trau loads and renders them
// into the cold verify prompt, and the agent — which inherits only the handoff,
// the code, and these checks on disk — executes each one and reports it back. This
// keeps verify a single fresh, adversarial process and gives a provider-agnostic
// contract that a future cross-vendor verify panel can fan out unchanged.
package checks

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Dir is the conventional in-repo location for custom checks, relative to the
// target repo root.
const Dir = ".trau/checks"

// Severity levels. An error-severity check that fails blocks the merge; a
// warn-severity check that fails is reported but non-blocking.
const (
	SeverityError = "error"
	SeverityWarn  = "warn"
)

// Check is one named verification the cold verify phase must run. A check is
// either deterministic (Command: a shell command that passes on exit status 0) or
// agent-judged (Prompt: a question the verifier answers from the code and diff);
// exactly one of the two is set. Tools optionally constrains which tools the
// verifier may use when evaluating a prompt check.
type Check struct {
	Name     string   `yaml:"name"`
	Severity string   `yaml:"severity"`
	Command  string   `yaml:"command"`
	Prompt   string   `yaml:"prompt"`
	Tools    []string `yaml:"tools"`
}

// NormalizeSeverity lower-cases and canonicalizes a severity. Anything that is
// not an explicit warning is treated as error, so an unlabeled or misspelled
// check fails closed (it gates the merge rather than being silently downgraded).
func NormalizeSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case SeverityWarn, "warning":
		return SeverityWarn
	default:
		return SeverityError
	}
}

// Blocks reports whether a failing check of this severity blocks the merge.
func Blocks(severity string) bool { return NormalizeSeverity(severity) == SeverityError }

// Load reads the custom check library from <repoRoot>/.trau/checks (*.yml and
// *.yaml, in filename order). It returns the built-in Defaults when the directory
// is absent or holds no checks, with usedDefaults=true so callers can report which
// set ran. Severities are normalized and the set is validated before returning.
func Load(repoRoot string) (list []Check, usedDefaults bool, err error) {
	dir := filepath.Join(repoRoot, Dir)
	entries, derr := os.ReadDir(dir)
	if derr != nil {
		if os.IsNotExist(derr) {
			return Defaults(), true, nil
		}
		return nil, false, fmt.Errorf("read checks dir %s: %w", dir, derr)
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".yml", ".yaml":
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		data, rerr := os.ReadFile(filepath.Join(dir, name))
		if rerr != nil {
			return nil, false, fmt.Errorf("read check %s: %w", name, rerr)
		}
		parsed, perr := parseFile(data)
		if perr != nil {
			return nil, false, fmt.Errorf("parse check %s: %w", name, perr)
		}
		list = append(list, parsed...)
	}

	if len(list) == 0 {
		return Defaults(), true, nil
	}
	for i := range list {
		list[i].Severity = NormalizeSeverity(list[i].Severity)
	}
	if verr := validate(list); verr != nil {
		return nil, false, verr
	}
	return list, false, nil
}

// parseFile accepts three shapes for a check file: a single check mapping, a bare
// sequence of checks, or a {checks: [...]} wrapper. yaml.v3 errors when a document
// is decoded into the wrong kind, which lets the wrapper and sequence forms fall
// through to the single-mapping form.
func parseFile(data []byte) ([]Check, error) {
	if strings.TrimSpace(string(data)) == "" {
		return nil, nil
	}
	var wrap struct {
		Checks []Check `yaml:"checks"`
	}
	if err := yaml.Unmarshal(data, &wrap); err == nil && len(wrap.Checks) > 0 {
		return wrap.Checks, nil
	}
	var seq []Check
	if err := yaml.Unmarshal(data, &seq); err == nil && len(seq) > 0 {
		return seq, nil
	}
	var one Check
	if err := yaml.Unmarshal(data, &one); err != nil {
		return nil, err
	}
	if one.Name == "" && one.Command == "" && one.Prompt == "" {
		return nil, nil
	}
	return []Check{one}, nil
}

// validate enforces the check contract: a unique name and exactly one of command
// or prompt per check.
func validate(list []Check) error {
	seen := make(map[string]bool, len(list))
	for _, c := range list {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			return fmt.Errorf("check missing a name")
		}
		if seen[name] {
			return fmt.Errorf("duplicate check name %q", name)
		}
		seen[name] = true
		hasCmd := strings.TrimSpace(c.Command) != ""
		hasPrompt := strings.TrimSpace(c.Prompt) != ""
		if hasCmd == hasPrompt {
			return fmt.Errorf("check %q must set exactly one of command or prompt", name)
		}
	}
	return nil
}

// Defaults is the built-in check set that runs when a repo declares no custom
// checks — the "Ralph prompt stdlib". They are deliberately prompt checks, not
// hard-coded commands, so each verifier discovers the project's own test runner,
// type checker, and linter from the repo and trau stays app-agnostic.
func Defaults() []Check {
	return []Check{
		{
			Name:     "tests",
			Severity: SeverityError,
			Prompt:   "Run the project's own test runner on the tests relevant to this slice (the new or changed test files for this ticket) — discover the runner from the repo and run only those tests, not the whole suite. Pass only if every relevant test passes.",
		},
		{
			Name:     "typecheck",
			Severity: SeverityError,
			Prompt:   "If the project has a type checker or static analyzer (e.g. tsc, mypy, phpstan, go vet), run it over the changed code. Pass if it reports no new errors in this slice; pass without running if the project has none.",
		},
		{
			Name:     "lint",
			Severity: SeverityWarn,
			Prompt:   "If the project has a linter or formatter (e.g. eslint, golangci-lint, ruff, pint), run it over the changed files. Pass if clean; pass without running if none is configured.",
		},
		{
			Name:     "anti-placeholder",
			Severity: SeverityError,
			Prompt:   "Inspect this slice's diff for placeholder or stubbed code shipped as if finished: TODO/FIXME left where behavior was required, functions returning hard-coded or fake values, empty handlers, or commented-out stubs. Fail if the slice ships a placeholder in place of the real behavior the ticket asked for.",
		},
		{
			Name:     "anti-duplication",
			Severity: SeverityWarn,
			Prompt:   "Inspect this slice's diff for copy-paste duplication that should have been factored out — repeated blocks of this slice's own logic a small helper would remove. Fail on egregious duplication introduced by this slice; ignore pre-existing duplication.",
		},
	}
}

// Render produces the verify-prompt fragment that enumerates the checks the cold
// verifier must run and tells it how to report each result in the verdict. It
// returns the empty string for an empty library, so the caller renders no checks
// section at all.
func Render(list []Check) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(" VERIFICATION CHECKS — additionally run every check below and report each one. ")
	b.WriteString(`A check's severity sets its weight: an "error" check that fails BLOCKS the merge; a "warn" check that fails is reported but does not block.`)
	b.WriteString("\n")
	for i, c := range list {
		fmt.Fprintf(&b, "%d. [%s] %s — ", i+1, NormalizeSeverity(c.Severity), strings.TrimSpace(c.Name))
		if cmd := strings.TrimSpace(c.Command); cmd != "" {
			fmt.Fprintf(&b, "run the command `%s`; it passes on exit status 0 and fails otherwise.", cmd)
		} else {
			b.WriteString(strings.TrimSpace(c.Prompt))
		}
		if len(c.Tools) > 0 {
			fmt.Fprintf(&b, " (use only these tools: %s)", strings.Join(c.Tools, ", "))
		}
		b.WriteString("\n")
	}
	b.WriteString(`In the JSON verdict add a "checks" array with one object per check above: {"name":"...","severity":"error|warn","pass":true|false,"detail":"one line"}. `)
	b.WriteString(`Set the top-level "pass" to false if any "error" check fails or any brief behavior does not hold, and list each failing check and behavior in "failures".`)
	return b.String()
}
