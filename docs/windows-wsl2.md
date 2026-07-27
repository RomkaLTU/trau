# trau on Windows via WSL2

WSL2 is how you run trau on Windows today. The distro runs trau's ordinary Linux binaries —
nothing is emulated, nothing is ported — and it is the only Windows configuration where Claude
Code can sandbox what the agent runs ([ADR 0023](adr/0023-platform-support-windows.md)). A native
Windows build is in progress and will ship experimental; until then this is the whole story.

You need **WSL2** (not WSL1) with a distro installed. WSL2 runs on Windows 11 and on Windows 10
1903 / build 18362.1049+ on x64, 2004 / build 19041+ on Arm64 — anything older can only run WSL1.
From 2004 up, `wsl --install` in PowerShell sets up everything at once; 1903 and 1909 need
Microsoft's [manual install steps](https://learn.microsoft.com/windows/wsl/install-manual). Use
`wsl -l -v` to check the version of a distro you already have. Everything below runs **inside the
distro's shell**, never from PowerShell or CMD.

## 1. Install trau

Homebrew (4.5+ — earlier versions won't install a cask on Linux):

```bash
brew install --cask RomkaLTU/trau/trau
trau --version
```

Without Homebrew, take the `.deb` or `.rpm` for your architecture from the
[latest release](https://github.com/RomkaLTU/trau/releases/latest) — both install `trau` to
`/usr/bin`:

```bash
sudo dpkg -i trau_<version>_linux_amd64.deb    # Debian, Ubuntu
sudo rpm -i trau_<version>_linux_amd64.rpm     # Fedora, RHEL, openSUSE
```

trau's other prerequisites — `git`, `gh`, `jq` — come from the distro's package manager
(`sudo apt install git gh jq`), and `gh` still needs `gh auth login` once.

## 2. Install Claude Code in the distro

Install the **Linux** build inside WSL, per
[Claude Code's own setup docs](https://code.claude.com/docs/en/setup). A Claude Code installed on
the Windows side is a different program on a different PATH; trau spawns whatever `claude` the
distro's shell resolves, so that is the one that has to exist and be signed in.

```bash
curl -fsSL https://claude.ai/install.sh | bash
claude          # sign in here, in the distro
```

Its login opens a browser the same way trau's hub does; if nothing comes up, see §4.

## 3. Keep target repos on the Linux filesystem

Clone the repos you point trau at under your home directory (`~/code/my-repo`), **never** under
the Windows mount (`/mnt/c/Users/...`). Microsoft recommends against working across the two file
systems at all, and both file access and search degrade badly on the mount — which is most of what
an agent does all day. A repo on `/mnt/c` is an unsupported configuration for trau.

Moving an existing checkout means cloning it again inside WSL, not copying it across the mount.
To see WSL files from Windows, run `explorer.exe .` in the repo, or browse `\\wsl$`.

## 4. The hub opens in your Windows browser

`trau` autostarts the web hub and prints its address (`Web UI: http://127.0.0.1:8728`). WSL2
forwards localhost, so that URL works unchanged in the Windows browser — there is nothing to
forward and no bind to widen. Keep the hub on its loopback default; exposing it is a separate
safety decision (see the README's *The web hub* section).

Hand-off from inside the distro tries `$BROWSER`, then `xdg-open`, then `wslview`, and prints the
URL either way — a missing opener costs you a click, not the hub. `wslview` comes from `wslu`
(`sudo apt install wslu`), and `$BROWSER` wins over both if you want a specific Windows browser:

```bash
export BROWSER='/mnt/c/Program Files/Google/Chrome/Application/chrome.exe'
```

## 5. Sandboxing

Claude Code's sandbox runs on WSL2 and not on native Windows, so WSL2 is the Windows path to pick
if you want the agent's commands contained (it needs `bubblewrap` and `socat` from your package
manager); trau assumes a trusted machine either way and runs the agent with your own credentials.
