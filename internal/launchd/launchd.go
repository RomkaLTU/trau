// Package launchd manages the macOS LaunchAgent that keeps the hub running.
// Nothing else restarts a killed hub: autostart only fires on the next
// interactive session (ADR 0004) and a loop child deliberately never resurrects
// the hub it reports to (ADR 0008). A user agent with KeepAlive closes that gap —
// launchd owns the process, so a crash or a kill brings it straight back — and
// stays entirely opt-in, installed by `trau hub supervise` and never implicitly.
package launchd

import (
	"encoding/xml"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Label identifies the agent to launchd and names its plist. It is derived from
// the module path, so it cannot collide with an unrelated agent.
const Label = "com.github.romkaltu.trau.hub"

// State is what the machine currently has installed: whether the plist exists,
// whether launchd holds the job, and the command the plist runs — enough for
// doctor to tell supervision apart from an agent pointing at a binary that moved.
type State struct {
	Installed bool
	Loaded    bool
	Program   []string
}

// ErrUnsupported is returned on every platform but macOS, where launchd does not
// exist and supervision is the init system's business instead.
var ErrUnsupported = errors.New("hub supervision needs launchd, which only macOS has")

// Supported reports whether this machine has launchd at all.
func Supported() bool { return runtime.GOOS == "darwin" }

// PlistPath is where the agent's plist lives — the per-user LaunchAgents
// directory, so supervision needs no root and follows the user's login session.
func PlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
}

// Read reports what is installed. A missing plist is not an error — it is the
// default state, and the answer doctor and the restart path both want.
func Read() (State, error) {
	path, err := PlistPath()
	if err != nil {
		return State{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read %s: %w", path, err)
	}
	return State{Installed: true, Loaded: loaded(), Program: programArguments(raw)}, nil
}

// Install writes the plist and hands the job to launchd, replacing any earlier
// one so a re-run adopts a moved binary or a changed environment. args are the
// arguments the hub is started with, env the variables it inherits — launchd
// gives an agent a minimal environment, so the PATH a hub-spawned loop needs to
// find git, gh, and the provider CLIs has to be captured here.
func Install(exe string, args []string, env map[string]string, logPath string) error {
	if !Supported() {
		return ErrUnsupported
	}
	path, err := PlistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, plist(exe, args, env, logPath), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// An agent already loaded refuses a second bootstrap, and the running job
	// would keep the old plist, so the replacement is always unload-then-load.
	_ = run("bootout", target())
	if err := run("bootstrap", domain(), path); err != nil {
		return fmt.Errorf("load the LaunchAgent: %w", err)
	}
	return nil
}

// Uninstall stops the job and removes the plist, leaving the machine exactly as
// it was before Install. An agent launchd does not hold is not an error.
func Uninstall() error {
	if !Supported() {
		return ErrUnsupported
	}
	path, err := PlistPath()
	if err != nil {
		return err
	}
	_ = run("bootout", target())
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// Kickstart starts the job, restarting it when it is already up. It is how a
// supervised hub is brought back: spawning one directly would produce a process
// launchd does not own, racing the one it is about to start itself.
func Kickstart() error {
	if !Supported() {
		return ErrUnsupported
	}
	if err := run("kickstart", "-k", target()); err != nil {
		return fmt.Errorf("kickstart the LaunchAgent: %w", err)
	}
	return nil
}

func domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func target() string { return domain() + "/" + Label }

func loaded() bool { return run("print", target()) == nil }

func run(args ...string) error {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err == nil {
		return nil
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return fmt.Errorf("launchctl %s: %w: %s", args[0], err, msg)
	}
	return fmt.Errorf("launchctl %s: %w", args[0], err)
}

// plist renders the agent. KeepAlive is unconditional — the hub is a daemon, so
// every exit, clean or not, is a reason to bring it back — and RunAtLoad starts
// it at login without waiting for the first restart.
func plist(exe string, args []string, env map[string]string, logPath string) []byte {
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	b.WriteString("\t<key>Label</key>\n\t<string>" + escape(Label) + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range append([]string{exe}, args...) {
		b.WriteString("\t\t<string>" + escape(a) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	if len(env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, k := range slices.Sorted(maps.Keys(env)) {
			b.WriteString("\t\t<key>" + escape(k) + "</key>\n\t\t<string>" + escape(env[k]) + "</string>\n")
		}
		b.WriteString("\t</dict>\n")
	}
	if logPath != "" {
		b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + escape(logPath) + "</string>\n")
		b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + escape(logPath) + "</string>\n")
	}
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	b.WriteString("</dict>\n</plist>\n")
	return []byte(b.String())
}

func escape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// programArguments pulls the command back out of an installed plist. The dict is
// a flat key/value sequence, so the value of a key is simply the node after it.
func programArguments(raw []byte) []string {
	var doc struct {
		Dict struct {
			Nodes []struct {
				XMLName xml.Name
				Text    string   `xml:",chardata"`
				Strings []string `xml:"string"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	nodes := doc.Dict.Nodes
	for i, node := range nodes {
		if node.XMLName.Local != "key" || strings.TrimSpace(node.Text) != "ProgramArguments" {
			continue
		}
		if i+1 < len(nodes) {
			return nodes[i+1].Strings
		}
	}
	return nil
}
