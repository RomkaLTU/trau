package prompts

const buildDefault = `Implement {{.ID}} on branch {{.Branch}} (already checked out). {{.SkillsNote}}{{.Note}} Implement the ticket fully and run only the tests relevant to this slice (the new or changed test files for this ticket) — not the entire suite. In a multi-workspace repo (monorepo), work inside the workspace(s) the ticket concerns and use their own commands, scoped to those workspaces, rather than repo-wide runs.{{.CodeStyle}} Do not commit, push, or open a PR — stop after implementation. If the ticket clearly belongs to a DIFFERENT repository or codebase — the files, directories, or stack it references do not exist here and are not something this ticket asks you to create (in a multi-workspace repo, check every workspace before concluding this: a ticket for any of its apps or packages belongs here) — do NOT implement anything and do NOT modify any files; end your reply with a final line 'REFUSED: <one short sentence naming what the ticket actually targets>'.{{.BuildNotes}}{{.TicketContext}}`

const handoffDefault = `Write a QA brief for {{.ID}}: the concrete, checkable behaviors a manual QA tester must verify for this slice, in priority order. Don't duplicate content already in the ticket, PRD, or diff — focus on what to check and how. Do NOT run the test suite, execute the code, or verify behavior yourself — a separate verify step does that; just write the brief. Redact any secrets. Save it to exactly {{.Handoff}} (overwrite if present) and nowhere else.{{.Rubric}}{{.TicketContext}}`

const verifyDefault = `Cold, adversarial QA verification of {{.ID}} against {{if .Handoff}}the QA brief at {{.Handoff}}{{else}}the ticket below and this slice's diff against the base branch{{end}}. Treat {{if .Handoff}}the code on disk and the brief{{else}}the code on disk and the ticket{{end}} as the only sources of truth; your job is to find what does NOT work.{{.RubricNote}}{{.LessonsNote}} Run only the tests relevant to this slice (the new or changed test files for this ticket) using the project's test runner (in a multi-workspace repo, the affected workspace's own runner) — not the whole suite. {{if .Handoff}}For each behavior the brief lists, confirm it actually holds.{{else}}No separate QA brief was written for this tiny slice: derive the concrete, checkable behaviors yourself from the ticket and the diff, then confirm each actually holds.{{end}} {{.Note}} Distinguish defects in this slice's own code from pre-existing or out-of-scope issues. When finished, write a JSON verdict to exactly {{.Verdict}}: {"pass": true|false, "summary": "one line", "failures": ["..."]}. Set pass=false if any relevant test fails or {{if .Handoff}}any behavior in the brief does not work{{else}}any behavior the ticket requires does not work{{end}}; failures lists each concrete problem (empty when pass is true).{{.ChecksFragment}} Do not commit, push, or open a PR.{{.TicketContext}}`

const commitDefault = `Commit the implementation for {{.ID}}. Verify has already passed on this working tree — do NOT run tests, re-verify behavior, or re-analyze the diff for correctness; just stage and commit, and do NOT emit a status report (your final message is only the commit subject line(s)). Stage and commit ONLY files that are part of {{.ID}}; never commit unrelated untracked files or tooling (e.g. scripts/, *.env).{{.RubricNote}} For a small, single-purpose change (a bug fix plus its tests, or ≤~5 files) make ONE commit; split into atomic, dependency-ordered commits only for genuinely independent concerns.{{if .Squash}} The merge method is squash, so skip splitting entirely and make ONE commit.{{end}} Use Conventional Commits: '<type>(scope): <subject>' (type ∈ feat|fix|refactor|docs|style|test|chore), imperative mood, subject under 72 characters, with a 'Refs: {{.ID}}' trailer; match the project's existing git-log style if it differs. The commit message must contain ONLY the subject and body: do NOT add any 'Co-authored-by:'/'Co-Authored-By:' trailer, a '🤖 Generated with Claude Code' line, or any mention of AI/assistant authorship, and remove them if your environment adds them by default.`

const repairDefault = `{{.ID}} verification FAILED. QA verdict file: {{.Verdict}}. {{if .Handoff}}QA brief: {{.Handoff}}. {{end}}Failures:
{{.Fails}}

You are on branch {{.Branch}} with this slice's implementation uncommitted.{{.RubricNote}}{{.LessonsNote}}{{.NotesNote}} If this is a DEFECT IN THIS SLICE'S OWN code, find the root cause and fix it with minimal, targeted changes, then run the relevant tests to confirm. If the failure is actually a pre-existing or out-of-scope bug NOT caused by this slice, do NOT hack around it — change nothing and say so clearly.{{.CodeStyle}} Do not commit, push, or open a PR.{{.TicketContext}}`

const bugfixDefault = `{{.ID}} verification FAILED after initial quick repairs. QA verdict file: {{.Verdict}}. {{if .Handoff}}QA brief: {{.Handoff}}. {{end}}Failures:
{{.Fails}}

You are on branch {{.Branch}} with this slice's implementation uncommitted.{{.RubricNote}}{{.LessonsNote}}{{.NotesNote}} This is a comprehensive bug-fix pass: read the full verdict, identify every failure that is a DEFECT IN THIS SLICE'S OWN code, and fix ALL of them with minimal, targeted changes. Do not stop after the first fix. Run the relevant tests (and browser checks if applicable) to confirm every failure is resolved before finishing. If a failure is a pre-existing or out-of-scope bug NOT caused by this slice, do NOT hack around it — note it clearly.{{.CodeStyle}} Do not commit, push, or open a PR.{{.TicketContext}}`

const pushRepairDefault = "{{.ID}}'s commit is on the feature branch but `git push` was REJECTED by a local pre-push hook — a quality gate the repo runs before allowing a push (tests, linters, static analysis, etc.). This is deterministic feedback about the committed code, NOT an infra error. Rejection output:\n\n{{.HookOutput}}\n\nRead the output, find the root cause in THIS slice's code, and fix it with minimal, targeted changes. Then COMMIT the fix so it becomes part of what gets pushed — amend the existing commit or add a follow-up commit, matching the repo's commit style. If the failure is a pre-existing or out-of-scope problem NOT caused by this slice, do NOT hack around it — say so clearly and change nothing.{{.NotesNote}}{{.CodeStyle}} Do NOT run `git push` or open a PR yourself — the loop re-pushes once you finish."

const resolveConflictsDefault = "The branch {{.Branch}} is mid-merge with {{.Base}} and has conflicts. Resolve EVERY conflicted file so the branch combines its own work with the latest {{.Base}}: run `git diff --name-only --diff-filter=U` to list them, edit each to keep BOTH sides' intent (never drop this branch's feature work, and never drop {{.Base}}'s newer changes; when both sides carry the SAME change — e.g. {{.Base}} already received it as a squash-merge — keep exactly one copy), then `git add` each resolved file. Run the relevant tests to confirm the combined result builds. Do NOT run `git commit`, `git merge --continue`, push, or open a PR — leave the resolved merge staged for the loop to finalize. Refs: {{.ID}}."

const epicRepairDefault = `The CI checks on the epic PR {{.PRURL}} (branch {{.Branch}}) are failing. You are on {{.Branch}} with the full epic integrated against the base. Investigate the failing checks, find the root cause, and fix it with minimal, targeted changes anywhere in the epic's code so the whole suite passes; run the relevant tests locally to confirm. Commit the fix with a Conventional Commit ('fix(scope): <subject>', imperative mood, no 'Co-authored-by'/AI-authorship trailers) but do NOT push or merge — the loop pushes and merges. Refs: {{.EpicID}}.`

const cleanupDefault = "Before the QA verify step for {{.ID}}, clean up the code this slice added or changed (uncommitted on the current branch) so it reads as if a senior engineer on this project wrote it. Review only the diff for this slice against the base branch. Remove: explanatory or narrating comments (anything that restates what the code does), section-banner comments, ticket IDs left in comments, commented-out code, and dead or unreachable code the slice introduced. Simplify AI tells: over-defensive guards for cases that cannot occur, redundant nil/error checks the surrounding codebase does not itself use, and belt-and-suspenders boilerplate a human wouldn't bother to write. Keep a comment only where a genuinely non-obvious decision needs one, matching the file's existing comment density. This is behavior-preserving housekeeping: do NOT change program logic, rename public APIs, or touch code outside this slice's diff. Leave load-bearing code alone. Make the edits directly: do NOT list, count, or justify what you left unchanged, and do NOT emit a JSON or prose report. Leave the result uncommitted on disk — do NOT commit, push, open a PR, or touch the issue tracker. End with exactly one line: `trimmed N comments/lines across M files` or `no changes needed`.{{.NotesNote}}"

const lintFixDefault = `Before the QA verify step for {{.ID}}, auto-fix mechanical lint and formatting issues in this repository (already checked out) so verify isn't spent on style noise. Detect the project's OWN automated fixers from its config — package.json/composer.json scripts (lint:fix, format, pint, php-cs-fixer, eslint --fix, prettier --write), a Makefile target (fmt, lint-fix), a pre-commit config, or the language's standard formatter (gofmt/goimports, ruff --fix, rubocop -a) — and run only those, in autofix mode, over the working tree. Prefer scoping the run to the files changed on this branch; in a multi-workspace repo, use the fixers of the workspaces those files live in. Apply the fixes and leave them uncommitted on disk. Do NOT change program logic, do NOT hand-fix anything the tools cannot auto-correct (leave that for verify), and do NOT run the test suite, commit, push, open a PR, or touch the issue tracker. If the project has no automated fixer, make no changes and stop.`

const lessonsDistillDefault = `A repair experiment for {{.ID}} just ended ({{.Result}}; failure type: {{.FailureType}}). Evidence:
{{.Evidence}}

Distill the single most reusable lesson a FUTURE run on similar work should remember to avoid or fix this faster. One or two sentences, concrete and general — no ticket-specific identifiers, file paths, or IDs. Also give 1-4 short lowercase keyword tags. Write ONLY this JSON to exactly {{.Path}} (overwrite if present) and nowhere else: {{.Schema}}. Do not change any code, run tests, commit, push, or open a PR.`

const rubricDefault = ` Also distill a structured acceptance rubric for {{.ID}} as JSON to exactly {{.Path}} (overwrite if present) and nowhere else, with this exact shape: {{.Schema}}. Populate acceptance_criteria and non_goals from the ticket/PRD (what must hold, and what is explicitly out of scope); required_tests with the concrete test files or commands that must pass for this slice; ui_paths with the browser/UI routes to exercise (omit or leave empty for backend-only slices); and fail_conditions with the explicit conditions that must make verification fail. Keep every entry a single concrete, checkable line; do not duplicate the prose brief.`

const buildNotesDefault = ` As a best-effort aid to the later pipeline phases, after implementing jot a short build-notes file to exactly {{.Path}} (overwrite if present) and nowhere else: the files you touched, the exact test command you ran for this slice, and any non-obvious decisions a later phase would otherwise have to rediscover. Keep it to a few lines and redact any secrets. This is optional — skipping it breaks nothing.`

const timelogEstimateDefault = "Estimate how many MINUTES of focused SENIOR-developer effort the change for {{.ID}} represents — an estimate of HUMAN effort to write this, NOT your runtime. The work is on the current branch: {{.Files}} files changed, +{{.Additions}}/-{{.Deletions}} lines across {{.Commits}} commit(s); inspect `git diff` and the commits if it helps. Anchor to: config/typo/one-line 15-30; small single-file bug fix 30-60; bug fix with tests (2-4 files) 60-120; small feature 120-240; mechanical refactor across many files 120-240; feature spanning UI+API+DB 240-480; architectural change with deep design 480-1440. Judge by distinct concerns touched, not raw line count (generated/scaffolding counts for little). Write ONLY the integer number of minutes (digits, nothing else) to exactly {{.Path}} and nowhere else. Do NOT change any code, run tests, commit, push, or open a PR."

const preambleDefault = `[Unattended run] You are running headless inside an automated loop — no human is watching and no input is possible. Never call AskUserQuestion or wait for a reply. When a choice arises, take the most reasonable / recommended default, proceed, and note the assumption in one line. If you truly cannot proceed safely, stop and say why. Do ALL work inline in THIS single agent: the Agent and Workflow tools (subagent spawning and multi-agent fan-out) are intentionally disabled for this loop, because each phase already runs as its own isolated process and fanning out only multiplies token cost without adding any isolation. Do not try to spawn subagents or parallel workers; if you genuinely believe one is unavoidable, stop and explain why in your final summary instead of working around it. (The TaskCreate/TaskUpdate todo-list tools are fine — they do not spawn anything.)`

const explorePreambleDefault = `[Unattended run] You are running headless inside an automated loop — no human is watching and no input is possible. Never call AskUserQuestion or wait for a reply. When a choice arises, take the most reasonable / recommended default, proceed, and note the assumption in one line. If you truly cannot proceed safely, stop and say why. You may dispatch read-only exploration subagents (the Explore agent type) to investigate the codebase in parallel and keep your own context lean — but do the actual implementation and every write inline in THIS agent. Multi-agent fan-out (the Workflow tool) and write-capable subagents stay disabled: they multiply token cost and let unobserved workers mutate the tree. (The TaskCreate/TaskUpdate todo-list tools are fine — they do not spawn anything.)`

const codeStyleDefault = ` Write it the way a senior engineer on this project would: clean, idiomatic, and matching the surrounding file's conventions. Do NOT add explanatory or narrating comments — no comment that restates what the code does, no section banners, no ticket IDs in comments, no multi-line 'why' essays; let clear names carry the meaning and keep a comment only where a genuinely non-obvious decision truly needs one, matching the file's existing comment density rather than exceeding it. Skip the AI tells: no over-defensive guards for cases that can't occur, no redundant error/nil checks the codebase doesn't already use, no belt-and-suspenders boilerplate a human wouldn't bother to write.`

const skillsDefault = `{{if .Installed}}This is an unattended run: this repo has skills: {{join .Installed ", "}}. {{if .Required}}Load these required skills with the Skill tool before implementing: {{join .Required ", "}}; then load any of the remaining skills relevant to this ticket. Do NOT pause to ask which skills to load.{{else}}Load the ones relevant to this ticket with the Skill tool before implementing — do NOT pause to ask which skills to load.{{end}}{{else}}This is an unattended run: auto-select and load the project skills relevant to this ticket — do NOT pause to ask which skills to load. Infer the project's stack from its manifests and configs (package.json, composer.json, go.mod, pyproject.toml, and the like) rather than assuming any framework; in a multi-workspace repo (monorepo), read the manifests of the workspaces the ticket touches, not only the root's. Always include the project's test skill when one exists, and add the domain skills matching the areas the ticket actually touches.{{end}}`

var registry = []Prompt{
	{
		Name:        "build",
		Title:       "Build",
		Description: "Implementation-phase prompt: implement the ticket on its feature branch.",
		Placeholders: []Placeholder{
			{Field: "ID", Description: "ticket id", Required: true, Sample: "COD-4242"},
			{Field: "Branch", Description: "feature branch the work happens on", Required: true, Sample: "feature/sample-slice"},
			{Field: "SkillsNote", Description: "rendered skills-loading sentence", Sample: "Load the repo's skills with the Skill tool before implementing."},
			{Field: "Note", Description: "resume/lessons fragment", Sample: " Lessons from earlier runs: none."},
			{Field: "CodeStyle", Description: "rendered code_style fragment", Sample: " Write it the way a senior engineer on this project would."},
			{Field: "BuildNotes", Description: "rendered build_notes fragment", Sample: " Jot a short build-notes file to runs/sample/notes.md."},
			{Field: "TicketContext", Description: "injected ticket content block", Sample: "\n\n=== TCK-7: Sample ticket ===\nSample ticket body.\n=== end TCK-7 ==="},
		},
		Default: buildDefault,
	},
	{
		Name:        "handoff",
		Title:       "Handoff",
		Description: "QA-brief authoring prompt feeding the cold verify phase.",
		Placeholders: []Placeholder{
			{Field: "ID", Description: "ticket id", Required: true, Sample: "COD-4242"},
			{Field: "Handoff", Description: "QA brief file path the loop reads back", Required: true, Sample: "runs/sample/handoff.md"},
			{Field: "Rubric", Description: "rendered rubric fragment", Sample: " Also distill an acceptance rubric to runs/sample/rubric.json."},
			{Field: "TicketContext", Description: "injected ticket content block", Sample: "\n\n=== TCK-7: Sample ticket ===\nSample ticket body.\n=== end TCK-7 ==="},
		},
		Default: handoffDefault,
	},
	{
		Name:        "verify",
		Title:       "Verify",
		Description: "Cold, adversarial QA verification prompt grading the slice.",
		Placeholders: []Placeholder{
			{Field: "ID", Description: "ticket id", Required: true, Sample: "COD-4242"},
			{Field: "Verdict", Description: "JSON verdict file path the loop parses", Required: true, Sample: "runs/sample/verdict.json"},
			{Field: "Handoff", Description: "QA brief file path; empty switches to derive-from-ticket wording", Sample: "runs/sample/handoff.md"},
			{Field: "Note", Description: "browser/app note", Sample: " The app runs at http://localhost:3000."},
			{Field: "ChecksFragment", Description: "deterministic verify-checks fragment", Sample: " Deterministic checks: the build passes."},
			{Field: "RubricNote", Description: "rubric pointer note", Sample: " A structured rubric is at runs/sample/rubric.json."},
			{Field: "LessonsNote", Description: "recalled-lessons note", Sample: " Lessons from similar runs: check both themes."},
			{Field: "TicketContext", Description: "injected ticket content block", Sample: "\n\n=== TCK-7: Sample ticket ===\nSample ticket body.\n=== end TCK-7 ==="},
		},
		Default: verifyDefault,
	},
	{
		Name:        "commit",
		Title:       "Commit",
		Description: "Stage-and-commit prompt enforcing Conventional Commits.",
		Placeholders: []Placeholder{
			{Field: "ID", Description: "ticket id", Required: true, Sample: "COD-4242"},
			{Field: "RubricNote", Description: "rubric non-goals pointer note", Sample: " Non-goals are listed in the rubric."},
			{Field: "Squash", Description: "true when the merge method is squash", Sample: true},
		},
		Default: commitDefault,
	},
	{
		Name:        "repair",
		Title:       "Repair",
		Description: "Targeted fix prompt after a failed verify.",
		Placeholders: []Placeholder{
			{Field: "ID", Description: "ticket id", Required: true, Sample: "COD-4242"},
			{Field: "Verdict", Description: "JSON verdict file path", Required: true, Sample: "runs/sample/verdict.json"},
			{Field: "Branch", Description: "feature branch the work sits on", Required: true, Sample: "feature/sample-slice"},
			{Field: "Fails", Description: "verdict failure lines", Required: true, Sample: "- the widget endpoint returns 500"},
			{Field: "Handoff", Description: "QA brief file path; empty drops the brief reference", Sample: "runs/sample/handoff.md"},
			{Field: "RubricNote", Description: "rubric pointer note", Sample: " A structured rubric is at runs/sample/rubric.json."},
			{Field: "LessonsNote", Description: "recalled-lessons note", Sample: " Lessons from similar runs: check both themes."},
			{Field: "NotesNote", Description: "build-notes pointer note", Sample: " Build notes: runs/sample/notes.md."},
			{Field: "CodeStyle", Description: "rendered code_style fragment", Sample: " Write it the way a senior engineer on this project would."},
			{Field: "TicketContext", Description: "injected ticket content block", Sample: "\n\n=== TCK-7: Sample ticket ===\nSample ticket body.\n=== end TCK-7 ==="},
		},
		Default: repairDefault,
	},
	{
		Name:        "bugfix",
		Title:       "Bugfix",
		Description: "Comprehensive fix pass after initial quick repairs failed.",
		Placeholders: []Placeholder{
			{Field: "ID", Description: "ticket id", Required: true, Sample: "COD-4242"},
			{Field: "Verdict", Description: "JSON verdict file path", Required: true, Sample: "runs/sample/verdict.json"},
			{Field: "Branch", Description: "feature branch the work sits on", Required: true, Sample: "feature/sample-slice"},
			{Field: "Fails", Description: "verdict failure lines", Required: true, Sample: "- the widget endpoint returns 500"},
			{Field: "Handoff", Description: "QA brief file path; empty drops the brief reference", Sample: "runs/sample/handoff.md"},
			{Field: "RubricNote", Description: "rubric pointer note", Sample: " A structured rubric is at runs/sample/rubric.json."},
			{Field: "LessonsNote", Description: "recalled-lessons note", Sample: " Lessons from similar runs: check both themes."},
			{Field: "NotesNote", Description: "build-notes pointer note", Sample: " Build notes: runs/sample/notes.md."},
			{Field: "CodeStyle", Description: "rendered code_style fragment", Sample: " Write it the way a senior engineer on this project would."},
			{Field: "TicketContext", Description: "injected ticket content block", Sample: "\n\n=== TCK-7: Sample ticket ===\nSample ticket body.\n=== end TCK-7 ==="},
		},
		Default: bugfixDefault,
	},
	{
		Name:        "push_repair",
		Title:       "Push repair",
		Description: "Fix prompt for a pre-push hook rejection of the committed slice.",
		Placeholders: []Placeholder{
			{Field: "ID", Description: "ticket id", Required: true, Sample: "COD-4242"},
			{Field: "HookOutput", Description: "verbatim pre-push rejection output", Required: true, Sample: "pre-push: lint found 3 errors"},
			{Field: "NotesNote", Description: "build-notes pointer note", Sample: " Build notes: runs/sample/notes.md."},
			{Field: "CodeStyle", Description: "rendered code_style fragment", Sample: " Write it the way a senior engineer on this project would."},
		},
		Default: pushRepairDefault,
	},
	{
		Name:        "resolve_conflicts",
		Title:       "Resolve conflicts",
		Description: "Mid-merge conflict resolution prompt; the loop finalizes the merge.",
		Placeholders: []Placeholder{
			{Field: "ID", Description: "ticket id", Required: true, Sample: "COD-4242"},
			{Field: "Base", Description: "branch being merged in", Required: true, Sample: "epic/sample-epic"},
			{Field: "Branch", Description: "branch mid-merge", Required: true, Sample: "feature/sample-slice"},
		},
		Default: resolveConflictsDefault,
	},
	{
		Name:        "epic_repair",
		Title:       "Epic repair",
		Description: "Fix prompt for red CI on the epic PR.",
		Placeholders: []Placeholder{
			{Field: "EpicID", Description: "epic ticket id", Required: true, Sample: "COD-4200"},
			{Field: "PRURL", Description: "epic PR URL", Required: true, Sample: "https://github.com/acme/widgets/pull/17"},
			{Field: "Branch", Description: "epic integration branch", Required: true, Sample: "epic/sample-epic"},
		},
		Default: epicRepairDefault,
	},
	{
		Name:        "cleanup",
		Title:       "Cleanup",
		Description: "AI-slop cleanup pass over the slice's diff before verify.",
		Placeholders: []Placeholder{
			{Field: "ID", Description: "ticket id", Required: true, Sample: "COD-4242"},
			{Field: "NotesNote", Description: "build-notes pointer note", Sample: " Build notes: runs/sample/notes.md."},
		},
		Default: cleanupDefault,
	},
	{
		Name:        "lint_fix",
		Title:       "Lint fix",
		Description: "Automated lint/format fixer pass before verify.",
		Placeholders: []Placeholder{
			{Field: "ID", Description: "ticket id", Required: true, Sample: "COD-4242"},
		},
		Default: lintFixDefault,
	},
	{
		Name:        "lessons_distill",
		Title:       "Lessons distill",
		Description: "Distill a reusable lesson from a finished repair experiment.",
		Placeholders: []Placeholder{
			{Field: "ID", Description: "ticket id", Required: true, Sample: "COD-4242"},
			{Field: "Path", Description: "JSON file path the loop parses back", Required: true, Sample: "runs/sample/lesson.json"},
			{Field: "Schema", Description: "exact JSON skeleton to fill", Required: true, Sample: `{"lesson":"","tags":[]}`},
			{Field: "Result", Description: "experiment outcome", Sample: "failure"},
			{Field: "FailureType", Description: "classified failure type", Sample: "test"},
			{Field: "Evidence", Description: "evidence lines", Sample: "- verify failed twice on the same assertion"},
		},
		Default: lessonsDistillDefault,
	},
	{
		Name:        "rubric",
		Title:       "Rubric",
		Description: "Handoff fragment requesting the structured acceptance rubric.",
		Placeholders: []Placeholder{
			{Field: "ID", Description: "ticket id", Required: true, Sample: "COD-4242"},
			{Field: "Path", Description: "rubric JSON file path the loop reads back", Required: true, Sample: "runs/sample/rubric.json"},
			{Field: "Schema", Description: "exact rubric JSON shape", Required: true, Sample: `{"ticket":"","acceptance_criteria":[]}`},
		},
		Default: rubricDefault,
	},
	{
		Name:        "build_notes",
		Title:       "Build notes",
		Description: "Build fragment requesting best-effort notes for the mechanical phases.",
		Placeholders: []Placeholder{
			{Field: "Path", Description: "notes file path the mechanical phases read back", Required: true, Sample: "runs/sample/notes.md"},
			{Field: "ID", Description: "ticket id", Sample: "COD-4242"},
		},
		Default: buildNotesDefault,
	},
	{
		Name:        "timelog_estimate",
		Title:       "Timelog estimate",
		Description: "Post-merge senior-effort estimate in minutes.",
		Placeholders: []Placeholder{
			{Field: "ID", Description: "ticket id", Required: true, Sample: "COD-4242"},
			{Field: "Path", Description: "file path the loop parses the integer from", Required: true, Sample: "runs/sample/minutes.txt"},
			{Field: "Files", Description: "changed file count", Sample: 3},
			{Field: "Additions", Description: "added line count", Sample: 120},
			{Field: "Deletions", Description: "deleted line count", Sample: 45},
			{Field: "Commits", Description: "commit count", Sample: 2},
		},
		Default: timelogEstimateDefault,
	},
	{
		Name:        "preamble",
		Title:       "Preamble",
		Description: "Unattended-run preamble prepended to every headless prompt.",
		Default:     preambleDefault,
	},
	{
		Name:        "explore_preamble",
		Title:       "Explore preamble",
		Description: "Preamble variant permitting read-only Explore subagents.",
		Default:     explorePreambleDefault,
	},
	{
		Name:        "code_style",
		Title:       "Code style",
		Description: "Senior-engineer code-style fragment shared by build and repair prompts.",
		Default:     codeStyleDefault,
	},
	{
		Name:        "skills",
		Title:       "Skills",
		Description: "Skills-loading sentence for the build prompt.",
		Placeholders: []Placeholder{
			{Field: "Installed", Description: "installed skill names; empty falls back to self-selection", Sample: []string{"golang-pro", "web-feature"}},
			{Field: "Required", Description: "required skill names, already intersected with Installed", Sample: []string{"golang-pro"}},
		},
		Default: skillsDefault,
	},
}
