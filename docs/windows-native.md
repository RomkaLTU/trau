# trau on native Windows

The native Windows build is **experimental** ([ADR 0023](adr/0023-platform-support-windows.md)): it
runs unsandboxed, so the agent's commands hit your machine directly. For Claude Code sandboxing use
[WSL2](windows-wsl2.md) instead — same trau, the ordinary Linux binaries, inside the distro.

Everything below runs in **PowerShell** (or Windows Terminal's PowerShell profile), not in a WSL
shell. The Windows floor is Windows 10 1809 or later, amd64 and arm64.

## Install

Scoop, from trau's own bucket:

```powershell
scoop bucket add trau https://github.com/RomkaLTU/scoop-trau
scoop install trau
trau --version
```

Or winget, once the manifest for a release clears Microsoft's review:

```powershell
winget install Codesomelabs.trau
```

The binary is `trau.exe`, but you never type the extension: Windows resolves it from `PATHEXT`, so
every example in the docs — `trau serve`, `trau doctor`, `trau takeover <ID>` — works verbatim.
Scoop puts a shim at `%USERPROFILE%\scoop\shims\trau.exe`; winget links one into
`%LOCALAPPDATA%\Microsoft\WinGet\Links`. Both directories are on `PATH` after the installer runs.

From source there is no `make` to lean on, so run what `make build` runs — the SPA first, since the
binary embeds it — and name the output yourself, because the Makefile's `bin/trau` has no extension
for Windows to execute:

```powershell
git clone https://github.com/RomkaLTU/trau
cd trau
npm --prefix web ci
npm --prefix web run build
go build -o bin\trau.exe ./cmd/trau
```

## Prerequisites on PATH

trau spawns `git`, `gh`, `claude`, and `npx` **by bare name** and lets `exec.LookPath` resolve them
through `PATH` and `PATHEXT` — trau never spells an extension out, and only the agent CLI can be
pointed elsewhere (`CLAUDE_BIN`). So each one has to be on the `PATH` of the shell you start trau
from:

- **[Git for Windows](https://git-scm.com/download/win)** — the installer's "Git from the command
  line" option is what puts `git.exe` on `PATH`. Claude Code also needs Git Bash on native Windows;
  point it at a non-default install with `CLAUDE_CODE_GIT_BASH_PATH`.
- **[GitHub CLI](https://cli.github.com)** — `gh.exe`, authenticated once with `gh auth login`.
- **`claude`** (or `codex` / `kimi`) — Claude Code is natively supported and Authenticode-signed.
- **`npx`** — from Node.js, used to install and remove agent skills.

`trau doctor` resolves `git`, `gh` and your agent CLI the same way trau does and says which one it
could not find. If a tool works in your shell but trau cannot, the usual cause is a `PATH` entry an
installer added that the already-running shell never picked up — open a new one.

## Self-update

trau never replaces its own binary: whichever package manager installed it owns updating it. On
Windows the hub's **Updates** panel detects that and prints the command for the channel you used —
`scoop update trau` or `winget upgrade Codesomelabs.trau` — instead of the macOS `brew` flow, and
the one-click **Update now** button stays hidden. An install no manager owns (a `go build` result, a
zip unpacked by hand) degrades to a link to the
[releases page](https://github.com/RomkaLTU/trau/releases). Restart the hub after any of them so it
serves the version that landed.

## Open in terminal

The hub's **Open in terminal** button is macOS-only — the launch drives Terminal or iTerm over
osascript ([ADR 0018](adr/0018-terminal-takeover-handoff.md)) — so on Windows the run view does not
show it, and `TERMINAL_APP` is ignored. The handoff itself works everywhere: open a terminal and run

```powershell
trau takeover <ID>
```

It takes the same lock, resumes the same recorded claude session, and releases the repo when you
close it. The hub must already be running, since the lock is a presence entry it owns.

## Copying out of the TUI

The TUI's copy keys — `y` for the selected log, artifact or fault message, `Y` for its path — write
to the clipboard with OSC 52 escape sequences rather than any OS clipboard API. Windows Terminal has
supported OSC 52 copy since v1.2 in 2020 — long before any Windows build that can run trau — and is
the default host from Windows 11 22H2 on, so the keys work there unchanged. Two caveats:

- The **classic console host** — the window `cmd.exe` opens outside Windows Terminal on older
  Windows 10 — only gained OSC 52 in a 2025 servicing update. Where it has not landed, the copy is
  silently dropped: nothing errors, the clipboard just does not change. Run trau inside Windows
  Terminal.
- Clipboard *reads* over OSC 52 are deliberately not implemented in Windows Terminal. trau only
  ever writes, so this costs nothing, but a terminal-based editor you resume through a takeover may
  not be able to paste that way.
