package config

import (
	"slices"
	"strings"
	"testing"
)

// The --skip value is validated at startup, so a typo can never read as "skip
// nothing" and let work through the bar the operator thought they had lowered.
func TestParseSkips(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{name: "one key", in: "verify", want: []string{SkipVerify}},
		{name: "comma separated list", in: "verify,ci", want: []string{SkipVerify, SkipCI}},
		{name: "canonical order regardless of input order", in: "merge,lintfix", want: []string{SkipLintFix, SkipMerge}},
		{name: "spaces and case", in: " Verify , CI ", want: []string{SkipVerify, SkipCI}},
		{name: "repeats collapse", in: "ci,ci", want: []string{SkipCI}},
		{name: "empty value skips nothing", in: "", want: nil},
		{name: "every key", in: "lintfix,cleanup,verify,ci,merge", want: SkipKeys()},
		{name: "unknown key", in: "bogus", wantErr: true},
		{name: "one unknown key among valid ones", in: "verify,bogus", wantErr: true},
		{name: "a phase that has no key", in: "build", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSkips(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseSkips(%q) = %v, want an error", tc.in, got)
				}
				for _, key := range SkipKeys() {
					if !strings.Contains(err.Error(), key) {
						t.Errorf("error %q does not name the valid key %q", err, key)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSkips(%q): %v", tc.in, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("ParseSkips(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseArgsSkip(t *testing.T) {
	o, err := ParseArgs([]string{"--skip", "verify,ci", "COD-1519"})
	if err != nil {
		t.Fatalf("ParseArgs(--skip verify,ci COD-1519): %v", err)
	}
	if !slices.Equal(o.Skips, []string{SkipVerify, SkipCI}) {
		t.Errorf("Skips = %v, want [verify ci]", o.Skips)
	}
	if o.Parent != "COD-1519" {
		t.Errorf("Parent = %q, want COD-1519", o.Parent)
	}
}

// Two --skip occurrences describe one set, not the last one alone.
func TestParseArgsSkipRepeatedFlagMerges(t *testing.T) {
	o, err := ParseArgs([]string{"--skip", "merge", "--skip", "verify"})
	if err != nil {
		t.Fatalf("ParseArgs(--skip merge --skip verify): %v", err)
	}
	if !slices.Equal(o.Skips, []string{SkipVerify, SkipMerge}) {
		t.Errorf("Skips = %v, want [verify merge]", o.Skips)
	}
}

func TestParseArgsSkipRejectsUnknownKey(t *testing.T) {
	_, err := ParseArgs([]string{"--skip", "bogus"})
	if err == nil {
		t.Fatal("ParseArgs(--skip bogus) should error")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error %q does not name the rejected key", err)
	}
	for _, key := range SkipKeys() {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not name the valid key %q", err, key)
		}
	}
}

func TestParseArgsSkipRequiresValue(t *testing.T) {
	if _, err := ParseArgs([]string{"--skip"}); err == nil {
		t.Error("ParseArgs(--skip) without a value should error")
	}
}
