# ADR 0002 — Release & distribution strategy

- **Status:** Accepted
- **Date:** 2026-06-22
- **Deciders:** Romas (sole maintainer)
- **Refs:** COD-562; builds on ADR 0001 (standalone repo, independent release cadence)

## Context

`trau` is a standalone Go binary (ADR 0001) that we now want to ship as open
source. Today the project has:

- Origin `github.com/RomkaLTU/trau` carrying the **full ralph/goloop dev history**.
- A `Makefile` that builds a static `CGO_ENABLED=0`, `-trimpath`, `-s -w` binary and
  cross-compiles `darwin/arm64` + `linux/amd64`, stamping `-X main.version` from
  `git describe --tags --always --dirty`.
- `var version = "dev"` in `cmd/trau/main.go` (only `version`; no commit/date vars).
- Apache-2.0 `LICENSE`, `README.md`, `CONTRIBUTING.md` already in place.
- **No git tags, no GitHub Releases, no CI, no goreleaser config.**
- Conventional-commit history (`feat`/`fix`/`docs`/`style`/`chore` with scopes).

This ADR decides: how we clean history for OSS, how we version and tag, how we
publish binaries (GitHub Releases via goreleaser), how users install (Homebrew
tap), and how we handle macOS Gatekeeper.

## Decision

### 1. Repository history — squash-in-place via an orphan commit

Collapse the full development history into a single **"Initial public release"**
commit on the existing `RomkaLTU/trau` remote, rather than standing up a second
public repo. Rationale:

- Sole maintainer, pre-1.0, **no tags/releases and no external clones to break** —
  the usual cost of history rewrite (disrupting collaborators) does not apply.
- One repo is less overhead than the "private working repo + separate public repo"
  split; ADR 0001 already committed to this repo as the project's home.
- The full history mixes the ralph→goloop→trau rename churn and is not something we
  want to curate for public consumption.

Procedure (gated on a clean secret scan — see §6):

1. Run a secret scan over the **entire history** (not just the tip), e.g. `gitleaks
   detect`. Remediate anything found *before* squashing — squashing the tip does not
   purge secrets from old objects until the rewrite drops them.
2. Archive the pre-squash state for safety: `git bundle create ../trau-prehistory.bundle --all`
   kept privately (and/or push current `main` to a private `archive/full-history` branch
   that will not go public).
3. Create an orphan branch from the current tree, commit once, and replace `main`:
   `git checkout --orphan public && git commit -m "Initial public release" && \
    git branch -M public main && git push --force-with-lease origin main`.
4. Flip the repo to public only after steps 1–3 verify clean.

**This is the one irreversible step in this ADR; the approach is approved (2026-06-22),
but execution still pauses for a final go-ahead at the force-push.** Alternative
considered — fresh public repo, keep this private: rejected as double the maintenance
for a solo, pre-1.0 tool, but remains the fallback if we ever want to retain public
granular history from a chosen point forward.

### 2. Versioning — SemVer, starting at `v0.1.0`

- Adopt **Semantic Versioning** with annotated tags `vX.Y.Z`. The tag is the single
  source of truth: the Makefile and goreleaser both derive the version from it, so a
  tagged build stamps a real version and an untagged build stamps `dev`.
- Start in the **0.x** track to match the "experimental" status in the README:
  breaking changes are allowed on minor bumps while < 1.0, called out in the
  changelog. Promote to `v1.0.0` when the Claude path and the provider seam are
  considered stable.
- The **first cut is `v0.1.0-rc.1`** — a full dry run through goreleaser + the tap
  (published as a GitHub pre-release) to exercise the pipeline before the real
  `v0.1.0`.
- Pre-releases use `-rc.N` / `-beta.N` suffixes (e.g. `v0.2.0-rc.1`); goreleaser
  marks these as GitHub pre-releases automatically (`prerelease: auto`).

### 3. Changelog — generated from Conventional Commits

We already write Conventional Commits, so the changelog is generated, not
hand-maintained. goreleaser groups commits into **Features** (`feat`) and **Bug
fixes** (`fix`) and excludes `docs`/`test`/`chore`/`style` from release notes. No
`CHANGELOG.md` file for now; the GitHub Release body is the canonical changelog.
(If a committed `CHANGELOG.md` is wanted later, adopt release-please in a *unified*
goreleaser workflow — never as a separate chained workflow.)

### 4. Binaries & GitHub Releases — goreleaser

A `.goreleaser.yaml` (Appendix A) drives the release. Decisions baked in:

- **Build matrix:** `darwin` + `linux`, each `amd64` + `arm64` — i.e. add
  `darwin/amd64` (Intel Macs) and `linux/arm64` to today's two targets. **Windows is
  deferred:** `trau` orchestrates `git`/`gh` and a Unix-style dev loop; Windows
  support is unproven and not a launch requirement.
- **ldflags:** `-s -w -X main.version={{.Version}}` — matches the existing single
  `version` var. (Optional enrichment: add `commit`/`date` vars to `main.go` and
  stamp them too, for a fuller `--version`. Not required for launch.)
- **Artifacts:** per-target `tar.gz` archives + a `checksums.txt`. SBOM/provenance
  deferred to 1.0.
- **Release:** published (not draft), `prerelease: auto`.

Local validation before any tag: `goreleaser check` and
`goreleaser release --snapshot --clean`. **Done 2026-06-22** — `check` passes and a
snapshot builds all four targets, archives, `checksums.txt`, and the cask; the built
binary stamps `main.version` via ldflags. Full git history is also `gitleaks`-clean
(103 commits), clearing the §1 secret gate.

### 5. Distribution — Homebrew tap first, core later

- Stand up a tap repo **`RomkaLTU/homebrew-trau`**. goreleaser's `homebrew_casks:`
  block generates and pushes a **cask** to the tap on every release, so installation
  is `brew install --cask RomkaLTU/trau/trau` (and `brew upgrade` thereafter). A cask
  (not a formula) is goreleaser's current recommendation for shipping pre-built
  binaries — `brews:` is deprecated — and it lets us strip the macOS quarantine
  attribute on install (see §6).
- Pushing to a *second* repo needs a token `GITHUB_TOKEN` can't provide: create a
  fine-grained PAT with contents:write on `homebrew-trau`, stored as the
  `HOMEBREW_TAP_GITHUB_TOKEN` Actions secret on `trau`.
- **homebrew-core is deferred.** It requires notability/stability thresholds a fresh
  solo tool won't meet; revisit after traction and a 1.0.

### 6. macOS Gatekeeper — rely on Homebrew, defer notarization

Unsigned binaries trip Gatekeeper on download. We **defer Apple Developer ID signing
and notarization** for the 0.x line because:

- The primary install path is the Homebrew cask, whose **postflight hook strips the
  `com.apple.quarantine` attribute** on install (configured in `.goreleaser.yaml`) —
  tap users get a binary that runs without a Gatekeeper prompt.
- Direct GitHub-Release downloaders are the only ones who hit it; document the
  one-line workaround (`xattr -d com.apple.quarantine ./trau`) in the README.

Signing/notarization (Developer ID cert, `notarytool`, goreleaser `signs:`/`notarize:`)
is a tracked follow-up for `v1.0.0` if direct-download demand grows. The "secret
scan before going public" in §1 is the security gate that *is* required now.

### 7. CI — release workflow on tag push

Add `.github/workflows/release.yml` (Appendix B): on a `v*` tag push, check out with
full depth, set up Go 1.24, and run `goreleaser/goreleaser-action`, wired with
`GITHUB_TOKEN` and `HOMEBREW_TAP_GITHUB_TOKEN`. A separate PR-time CI workflow
(`build`/`vet`/`lint`) is a recommended companion but is out of scope for COD-562 and
tracked separately.

## Rollout sequence

1. Secret-scan full history; remediate (§6/§1).
2. Confirm OSS docs: LICENSE ✓, README ✓ (add Install + Gatekeeper note), CONTRIBUTING ✓.
3. Squash to clean history; flip repo public (§1) — pausing for go-ahead at the force-push.
4. Land `.goreleaser.yaml`; validate with `goreleaser check` + `--snapshot`.
5. Create `RomkaLTU/homebrew-trau`; add the `HOMEBREW_TAP_GITHUB_TOKEN` secret.
6. Land `release.yml`.
7. Tag `v0.1.0-rc.1`; push; verify the pre-release + the auto-generated tap formula; `brew install` end-to-end.
8. Once the rc looks good, tag `v0.1.0` for the first real release.

## Decisions locked (2026-06-22)

- **History** — squash-in-place on `RomkaLTU/trau` (§1), not a separate public repo.
- **Windows** — deferred; launch matrix is `darwin`+`linux` × `amd64`+`arm64` (§4).
- **Version vars** — keep `-X main.version` only; no `commit`/`date` for now (§4).
- **First tag** — `v0.1.0-rc.1` dry run through the full pipeline, then `v0.1.0` (§2).

## Consequences

- **Positive:** one-command install (`brew install`), reproducible cross-platform
  binaries with checksums, automated changelogs, clean public history, and a tag-driven
  release that needs no manual artifact building.
- **Negative / follow-ups:** history rewrite is irreversible (mitigated by the private
  bundle archive); unsigned macOS binaries need a documented workaround until 1.0
  notarization; a PR-time CI workflow and homebrew-core submission remain open.

## Appendix A — `.goreleaser.yaml` (proposed)

```yaml
# yaml-language-server: $schema=https://goreleaser.com/static/schema.json
version: 2
project_name: trau

before:
  hooks:
    - go mod tidy

builds:
  - id: trau
    main: ./cmd/trau
    binary: trau
    env:
      - CGO_ENABLED=0
    flags:
      - -trimpath
    ldflags:
      - -s -w -X main.version={{ .Version }}
    goos: [darwin, linux]
    goarch: [amd64, arm64]

archives:
  - id: trau
    formats: [tar.gz]
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"

checksum:
  name_template: "checksums.txt"

changelog:
  sort: asc
  use: github
  filters:
    exclude:
      - "^docs:"
      - "^test:"
      - "^chore:"
      - "^style:"
  groups:
    - title: Features
      regexp: '^.*?feat(\(.+\))??!?:.+$'
      order: 0
    - title: Bug fixes
      regexp: '^.*?fix(\(.+\))??!?:.+$'
      order: 1
    - title: Others
      order: 999

homebrew_casks:
  - name: trau
    repository:
      owner: RomkaLTU
      name: homebrew-trau
      branch: main
      token: "{{ .Env.HOMEBREW_TAP_GITHUB_TOKEN }}"
    homepage: "https://github.com/RomkaLTU/trau"
    description: "Autonomous, ticket-driven development loop"
    # Strip the macOS quarantine attribute so the unsigned binary runs (§6).
    hooks:
      post:
        install: |
          if system_command("/usr/bin/xattr", args: ["-h"]).exit_status == 0
            system_command "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "#{staged_path}/trau"]
          end

release:
  github:
    owner: RomkaLTU
    name: trau
  draft: false
  prerelease: auto
```

## Appendix B — `.github/workflows/release.yml` (proposed)

```yaml
name: release
on:
  push:
    tags: ["v*"]

permissions:
  contents: write

jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version: "1.24"
      - uses: goreleaser/goreleaser-action@v6
        with:
          version: "~> v2"
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          HOMEBREW_TAP_GITHUB_TOKEN: ${{ secrets.HOMEBREW_TAP_GITHUB_TOKEN }}
```
