package notify

import (
	"strings"
	"testing"
)

func TestCommandDarwin(t *testing.T) {
	name, args, ok := command("darwin", "trau", "COD-217 quarantined")
	if !ok {
		t.Fatal("darwin should be supported")
	}
	if name != "osascript" {
		t.Errorf("name = %q, want osascript", name)
	}
	if len(args) != 2 || args[0] != "-e" {
		t.Fatalf("args = %v, want [-e <script>]", args)
	}
	script := args[1]
	if !strings.Contains(script, `with title "trau"`) {
		t.Errorf("script missing title: %q", script)
	}
	if !strings.Contains(script, `display notification "COD-217 quarantined"`) {
		t.Errorf("script missing body: %q", script)
	}
}

func TestCommandLinux(t *testing.T) {
	name, args, ok := command("linux", "trau", "session ended — 3 merged")
	if !ok {
		t.Fatal("linux should be supported")
	}
	if name != "notify-send" {
		t.Errorf("name = %q, want notify-send", name)
	}
	want := []string{"--", "trau", "session ended — 3 merged"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestCommandFlattensMultilineBody(t *testing.T) {
	_, args, ok := command("darwin", "trau", "COD-9 quarantined\nstack trace:\n  boom")
	if !ok {
		t.Fatal("darwin should be supported")
	}
	script := args[1]
	if strings.ContainsAny(script, "\n\r") {
		t.Errorf("script must not contain raw newlines (breaks AppleScript): %q", script)
	}
	if !strings.Contains(script, `"COD-9 quarantined stack trace: boom"`) {
		t.Errorf("body not flattened to one line: %q", script)
	}
}

func TestCommandUnsupported(t *testing.T) {
	if _, _, ok := command("plan9", "trau", "hi"); ok {
		t.Error("plan9 should be unsupported (graceful no-op)")
	}
}

func TestAppleScriptStringEscaping(t *testing.T) {
	cases := map[string]string{
		`plain`:          `"plain"`,
		`say "hi"`:       `"say \"hi\""`,
		`back\slash`:     `"back\\slash"`,
		`both "\" mixed`: `"both \"\\\" mixed"`,
	}
	for in, want := range cases {
		if got := appleScriptString(in); got != want {
			t.Errorf("appleScriptString(%q) = %q, want %q", in, got, want)
		}
	}
}
