# trau

Autonomous ticket loop: trau picks a ready tracker ticket, runs it through a
build → verify → handoff pipeline via an AI coding agent, and opens a PR.

## Language

**Repo**:
The target codebase trau builds, branches, and opens PRs against — resolved at launch, not necessarily the shell's cwd. Identified on screen by its folder name. Several Repos can share one Project (e.g. the m4c repos), so the Repo is the only unambiguous "where am I" signal.
_Avoid_: project (that's the tracker binding), workspace, target, directory, cwd

**Project**:
The tracker (Linear/Jira) project a Repo is bound to via the `PROJECT` config key — it scopes the ready queue and guards cross-project runs. May be empty; never use it to identify which Repo trau is operating on.
_Avoid_: repo, board

**Provider**:
The AI coding backend that executes a phase — a vendor + its CLI. The known set is `claude`, `codex`, `kimi`. Selected by the `PROVIDER` config key or the `--provider` flag.
_Avoid_: agent, model, vendor, backend (when you mean the named provider)

**Model**:
The specific model a provider runs (e.g. `claude-opus-4-8`). One provider has one active model at a time, resolved per-provider (`ClaudeModel`/`CodexModel`/`KimiModel`). A Model never spans Providers — switching Provider switches which Model applies.
_Avoid_: provider, engine

**Route**:
A per-phase override that sends one pipeline phase to a specific provider/model instead of the default (e.g. run `verify` on codex while the default is claude). Distinct from the default Provider.
_Avoid_: override (bare), phase provider

**Fallback provider**:
The ordered failover chain (`FALLBACK_PROVIDERS`) tried when the primary Provider fails transiently mid-run. Not a user choice per run — an automatic recovery path.
_Avoid_: backup, secondary provider

**Provider override**:
An ephemeral, single-run swap of the default Provider chosen from the Run once screen before launching a ticket. Applies to that one run and reverts to the config default afterward. Changes only the default Provider — Routes and Fallback providers are unaffected.
_Avoid_: route (that's per-phase), fallback (that's failover), setting the provider (that's persisted config)
