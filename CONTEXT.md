# trau

Autonomous ticket loop: trau picks a ready tracker ticket, runs it through a
build → verify → handoff pipeline via an AI coding agent, and opens a PR.

## Language

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
