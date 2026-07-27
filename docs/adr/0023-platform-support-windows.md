# ADR 0023 — Platform support: native Windows port, WSL2 channel, Win10 1809+ floor

- **Status:** Accepted
- **Date:** 2026-07-27
- **Deciders:** Romas (sole maintainer)
- **Supersedes:** [ADR 0002](0002-release-and-distribution-strategy.md) §4 — the Windows deferral
- **Refs:** [COD-1263] (epic); `tasks/windows-support-research.md` (2026-07-20 audit, local)

## Context

ADR 0002 §4 deferred Windows on the grounds that trau orchestrates `git`/`gh`
and a Unix-style dev loop, so Windows was "unproven and not a launch
requirement". The 2026-07-20 audit of the tree found that assumption wrong in
one direction and right in another.

Wrong: the codebase is already close to portable. Across the 583 Go files in
`internal/` and `cmd/` there are exactly **two** compile-breaking lines, both
`SysProcAttr{Setpgid: true}` (`internal/webserver/supervisor.go`,
`cmd/trau/autostart.go`). There is **no cgo** anywhere — `modernc.org/sqlite`
and `CGO_ENABLED=0` throughout. The hub singleton is a TCP port bind, CLI↔hub is
HTTP over TCP; no unix sockets, no pid files, no file locks. Paths go through
`os.UserHomeDir`/`TRAU_HOME` and `filepath.Join`, and external tools are spawned
by bare name so `exec.LookPath` resolves `.exe` via PATHEXT.

Right: there is one genuine architectural gap. `creack/pty` compiles on Windows
but returns `ErrUnsupported` at runtime — ConPTY was never implemented and will
not be. Since the claude provider is `ClaudeInteractive`, a real in-process PTY
whose transcript feeds `watch`/vterm, the auth sniffer and terminal takeover,
the primary loop is dead on native Windows until that is replaced. Beneath it
sit smaller runtime breaks: `SIGTERM` graceful stop and signal-0 liveness both
fail on Windows (the latter silently — every live instance would read as dead),
and inter-phase handoff files are written to literal `/tmp` paths.

Externally everything needed is in place: Claude Code is natively supported and
Authenticode-signed on Win10 1809+, `git` and `gh` are first-class, bubbletea v2
runs on Windows, maintained Go ConPTY libraries exist in the Charm ecosystem we
already depend on, and charmbracelet/crush ships natively on that same stack —
proof the architecture ports.

The research proposed gating the native release channel on a WSL2 demand
signal. That gate is dropped: the port is being executed now.

## Decision

### 1. Native Windows is a supported target, shipped experimental

Windows joins darwin and linux as a supported platform, marked **experimental**
in release notes and README until the QA slice ([COD-1273]) passes a full loop
on Win11 + Windows Terminal. Experimental means the artifacts ship and the
platform is documented; it does not mean the port waits for demand.

Win10 and legacy conhost are the degraded corner of the matrix — VT support and
the bubbletea Windows resize behaviour are worse there — and QA records that
rather than blocking on it.

### 2. WSL2 is a supported alternative, and the only sandboxed one

WSL2 runs the existing linux binaries unmodified. What is missing is a way to
install them: the tap publishes a cask, and casks are macOS-only, so a WSL user
cannot install trau today despite the binaries existing. Two channels close
that — goreleaser `nfpms:` (.deb/.rpm attached to the GitHub Release) and a
Homebrew **formula** in the existing tap beside the cask.

**Corrected 2026-07-27 (COD-1265):** casks stopped being macOS-only in Homebrew
4.5.0, which supports casks shipping Linux binaries; ours already emits
`on_linux` amd64/arm64 blocks around a `binary` stanza, so the existing cask
installs on WSL2 unchanged. Only `nfpms:` was added, for users without Homebrew.
The formula is dropped — see ADR 0002 §5 for why it is both unnecessary and
harmful on macOS.

Two WSL properties are load-bearing enough to state as decisions rather than
leave to a README:

- **Target repos live on the Linux filesystem** (`~/...`), never on the `/mnt/c`
  mount. Both Microsoft and Anthropic document the performance and search
  degradation there; a repo on `/mnt/c` is an unsupported configuration.
- **Hub URLs need no forwarding.** WSL2's default NAT forwards localhost, so a
  hub inside WSL is reachable from the Windows browser at `localhost:<port>`
  with no configuration. Browser hand-off from inside WSL follows
  `$BROWSER` → `xdg-open` → `wslview`, and prints the URL unconditionally.

WSL2 is also the only Windows option with Claude Code sandboxing (§6).

### 3. Platform floor: Windows 10 1809+

ConPTY landed in Windows 10 1809, and 1809 is also Claude Code's own floor.
Since trau cannot run a loop without both, the floor is exactly that — one
number, not a per-feature matrix. Server 2019 follows from the same line.
amd64 and arm64 are both supported; with no cgo, arm64 is free.

### 4. PTY: ConPTY behind the existing `terminalStarter` seam

`creack/pty` is retired. The replacement is
`github.com/aymanbagabas/go-pty` — cross-platform, `exec.Cmd`-shaped, and
maintained by a Charm engineer — with `github.com/charmbracelet/x/conpty` as
the fallback if it disappoints. The swap happens **behind the existing
`terminalStarter`/`terminalSession` seam**, which already isolates the PTY from
the agent: `startPTY` (`internal/agent/agent.go`) is the only production
implementation behind that seam, and the claude, codex and kimi backends all
default to it, so one replacement covers every interactive provider. The agents
themselves touch nothing but `terminalSession`, which is what makes this a
localized change rather than a rewrite.

The binding constraint is that **Unix behaviour must remain byte-identical** —
the transcript sink, window resize, and stall detection are all fed by these
bytes, and vterm reconstructs screens from them. A ConPTY transcript is A/B'd
against a macOS one for the same session before the swap is accepted.

Rejected: adding a pipe-mode (`claude -p`) fallback for Windows. Headless
spawning would sidestep ConPTY entirely, but it would give Windows a different
loop from every other platform — no PTY transcript, so no `watch`, no takeover,
no auth sniffing. That is a behaviour change wearing a port's clothes.

### 5. Distribution: zip archives, scoop bucket, winget portable

goreleaser gains `windows` targets for amd64 and arm64, archived as **zip**
rather than tar.gz, published through two channels:

- **scoop** — an own bucket repo, generated and pushed on release like the cask.
- **winget** — a portable manifest via PR into `microsoft/winget-pkgs`, the same
  route Claude Code itself ships. The portable/zip type is free; MSI is not.

**Chocolatey is rejected**: it gates on manual review and its push needs a
Windows runner, which would mean a second CI platform for one channel.

**Code signing is deferred.** Neither scoop nor winget requires it, and
SmartScreen mostly bites browser-downloaded executables rather than
package-manager installs. Revisit via SignPath Foundation (free for OSS) if
SmartScreen complaints actually appear.

### 6. Sandboxing: native Windows runs unsandboxed

Claude Code offers no sandboxing on native Windows; WSL2 is the only Windows
configuration that has it. trau does not treat that as a blocker — it already
assumes a trusted machine and runs the agent with the operator's own
credentials on macOS and linux. It gets **one line in the docs** stating the
posture and pointing users who want sandboxing at WSL2, not a capability gate.

### 7. Windows daemonization and process trees

Reserved. [COD-1269] appends the process-control design here — graceful stop and
liveness behind a seam, and the job-object semantics for the hub's child tree —
when that slice lands.

## Consequences

- The launch matrix from ADR 0002 §4 is superseded: darwin, linux and windows,
  each amd64 + arm64, with zip alongside tar.gz and three package channels
  (cask, nfpm, scoop/winget).
- Windows is a build target CI must keep honest. A `GOOS=windows go build ./...`
  cross-compile gate is cheap and stops the two `Setpgid` lines from coming back
  in a new form.
- Retiring `creack/pty` touches the one mechanism every interactive provider
  runs on. The unix-byte-identical constraint is what makes that acceptable, and
  it is a testable claim rather than a promise.
- The seam is not the whole retirement. `creack/pty` has a second import site
  outside it: `internal/usage/probe/pty.go` calls `pty.StartWithSize` directly
  to scrape the `/usage` panel. The §4 swap does not reach that call, so it has
  to be ported in the same slice or the opt-in PTY usage probe stays dead on
  Windows while the loop itself works.
- A second sandboxing story exists on one platform. Two supported Windows paths
  with different security properties is a documentation cost we take knowingly.
- Experimental status is a claim with an owner: it ends when [COD-1273] passes,
  not when the artifacts first build.

[COD-1263]: https://linear.app/codesomelabs/issue/COD-1263/epic-windows-support-native-windows-port
[COD-1269]: https://linear.app/codesomelabs/issue/COD-1269/cross-platform-process-control-graceful-stop-liveness-behind-a-seam
[COD-1273]: https://linear.app/codesomelabs/issue/COD-1273/native-windows-qa-full-loop-on-win11-windows-terminal-ship-as
