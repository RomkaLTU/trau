# ADR 0016 — Repo config beats home config

- **Status:** Accepted
- **Date:** 2026-07-16
- **Deciders:** Romas (sole maintainer)

## Context

The layered config originally resolved, from lowest to highest precedence:
defaults < `./trau.ini` < `<repo>/.trau.ini` < `~/.trau.ini` < env < CLI —
the home file was framed as "personal/machine overrides" and outranked the
repo's own config.

That inversion is wrong for every per-repo fact, and credentials made it bite:
a `JIRA_API_TOKEN` left in `~/.trau.ini` (valid for one Atlassian site)
silently shadowed a second repo's own, valid token for a different site. The
repo's doctor reported "Jira REST authentication failed" while the repo file's
credentials were demonstrably good, and sync could not infer the Jira provider
because `hasProjectJiraCreds` correctly demands the project layer as the
source — a signal the shadowing made unreachable. The failure is invisible by
construction: nothing in the repo explains it, and the repo file looks
authoritative while losing.

The multi-account Jira design (per-repo REST credentials, rest-only tracker)
already presumes the repo file is the authority for anything repo-specific.
Machine-wide knobs (models, effort, machine-trust flags) live at home because
no repo sets them — not because home must win when both do.

## Decision

More specific wins. The file layers resolve, lowest to highest:

defaults < `~/.trau.ini` < `./trau.ini` < `<repo>/.trau.ini` < env < CLI.

The home file is the personal/machine **baseline**; any repo that states a
value owns it. Environment variables and CLI flags still override everything,
so a one-off machine override remains a `TRAU_<KEY>` away.

## Consequences

- A stale global credential can no longer break a repo that carries its own;
  per-repo tracker identity works with zero global cleanup.
- Jira-provider inference (`hasProjectJiraCreds`) now sees the project layer
  whenever the repo supplies credentials, so a repo configured outside the
  wizard syncs as Jira without an explicit `TRACKER_PROVIDER`.
- A user who relied on `~/.trau.ini` to override a repo's committed-in-place
  value must use env/CLI for that; the settings surfaces report each value's
  source layer, so the resolution stays inspectable.
