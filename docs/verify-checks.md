# Verify checks (`.trau/checks`)

Trau's pipeline is fixed (build → handoff → **verify** → commit → PR → CI → merge),
but the *verify* phase is customizable: a target repo can declare named checks that
the cold, adversarial verifier must run and pass before a slice can merge. This lets
a project pin its own quality bar — "run my contract tests", "no `dd()` left in the
diff", "the OpenAPI spec still validates" — without forking the loop.

Checks are **run by the verify agent**, not by trau. Trau loads the checks, renders
them into the verify prompt, and the fresh verify process — which inherits only the
handoff brief, the code on disk, and these check files — executes each one and reports
the result back in its verdict. Trau then applies severity gating to the reported
results. This keeps verify a single cold process and means every provider (and, later,
every verifier in a cross-vendor panel) runs the identical check contract.

## Where checks live

In the **target repo** (the app trau is building), under:

```
.trau/checks/*.yaml      # or *.yml
```

Every `*.yaml`/`*.yml` file in that directory is loaded, in filename order. If the
directory is absent or empty, the built-in [default checks](#built-in-defaults) run
instead. Set `VERIFY_CHECKS=0` in `trau.ini` to turn the whole feature off and get the
original verify behavior.

## Check format

Each check has a name, a severity, and exactly one of `command` or `prompt`:

```yaml
name: contract-tests          # required, unique
severity: error               # error (blocks merge) | warn (surfaced, non-blocking)
command: composer test:contract   # a shell command; passes on exit 0, fails otherwise
# prompt: "..."               # OR an instruction the verifier judges (omit command)
# tools: [Read, Grep, Bash]   # optional: restrict tools for a prompt check
```

| Field      | Required | Meaning                                                                 |
|------------|----------|-------------------------------------------------------------------------|
| `name`     | yes      | Unique identifier; shown in logs and folded into the verdict.           |
| `severity` | no       | `error` (default) blocks the merge; `warn` is reported but non-blocking. Anything unrecognized is treated as `error` — checks fail closed. |
| `command`  | one of   | A deterministic shell command. Passes on exit status 0.                 |
| `prompt`   | one of   | An instruction the verifier evaluates against the code/diff.            |
| `tools`    | no       | For a prompt check, the only tools the verifier may use to evaluate it. |

A file may contain a single check (a mapping, as above), a list of checks (a YAML
sequence), or a `checks:` wrapper:

```yaml
checks:
  - name: no-debug-statements
    severity: error
    prompt: "Fail if the diff adds any dd(), dump(), console.log, or fmt.Println debug calls."
  - name: docs-updated
    severity: warn
    prompt: "Warn if a public API changed without a matching docs update."
```

## How results gate the merge

The verifier reports one result per check inside its JSON verdict:

```json
{
  "pass": false,
  "summary": "contract tests failed",
  "failures": ["[check:contract-tests] 2 contract tests red"],
  "checks": [
    {"name": "contract-tests", "severity": "error", "pass": false, "detail": "2 contract tests red"},
    {"name": "no-debug-statements", "severity": "error", "pass": true,  "detail": ""}
  ]
}
```

Trau then gates **authoritatively in Go**, using the severity declared in the check
file (not the one the agent echoed back, so a verifier can't downgrade a blocking
check):

- A failing **`error`** check forces the overall verdict to fail and is folded into
  `failures` — even if the agent set `pass: true`. This routes the slice to the
  existing self-heal/repair path, exactly like any other verify failure.
- A failing **`warn`** check is logged as a warning and does **not** block the merge.

## Built-in defaults

When a repo declares no custom checks, this set runs (the "Ralph prompt stdlib").
They are prompt checks, not hard-coded commands, so the verifier discovers the
project's actual test runner / type checker / linter and trau stays app-agnostic:

| Check              | Severity | What it does                                                        |
|--------------------|----------|---------------------------------------------------------------------|
| `tests`            | error    | Run the slice's relevant tests with the project's runner.           |
| `typecheck`        | error    | Run the type checker / static analyzer if the project has one.      |
| `lint`             | warn     | Run the linter / formatter if configured.                           |
| `anti-placeholder` | error    | Fail if the slice ships TODO/stub/fake-value placeholder code.      |
| `anti-duplication` | warn     | Flag egregious copy-paste duplication introduced by the slice.      |

Custom checks **replace** the defaults — once `.trau/checks` has any check, only the
checks you declare run. Copy the rows you still want into your own files.
