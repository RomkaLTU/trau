package config

import (
	"fmt"
	"slices"
	"strings"
)

// The canonical skip keys name the pipeline work a single run may bypass. Each
// one covers exactly one Activity of a Step (ADR 0037): SkipLintFix and
// SkipCleanup close out Build, SkipVerify closes the whole Verify Step, and
// SkipCI and SkipMerge close two Activities of Ship. Build, commit and PR carry
// no key at all — a run that produces nothing has nothing to skip.
const (
	SkipLintFix = "lintfix"
	SkipCleanup = "cleanup"
	SkipVerify  = "verify"
	SkipCI      = "ci"
	SkipMerge   = "merge"
)

// skipKeys is the canonical set in the order the error message lists it.
var skipKeys = []string{SkipLintFix, SkipCleanup, SkipVerify, SkipCI, SkipMerge}

// SkipKeys returns the canonical skip keys, for callers that name them to a human.
func SkipKeys() []string { return append([]string(nil), skipKeys...) }

// ParseSkips reads one comma-separated --skip value into the canonical keys it
// names. It rejects an unknown key naming the valid set, so a typo fails at
// startup rather than silently running the work the operator meant to bypass.
// Repeats collapse and the result keeps canonical order, so the same request
// always records the same set.
func ParseSkips(v string) ([]string, error) {
	named := map[string]bool{}
	for _, raw := range strings.Split(v, ",") {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" {
			continue
		}
		if !ValidSkip(key) {
			return nil, fmt.Errorf("--skip: unknown key %q; valid keys are %s", raw, strings.Join(skipKeys, ", "))
		}
		named[key] = true
	}
	return canonicalSkips(named), nil
}

// mergeSkips folds a second --skip occurrence into the set an earlier one
// produced, keeping the canonical order and dropping the repeats.
func mergeSkips(have, add []string) []string {
	named := map[string]bool{}
	for _, key := range append(append([]string(nil), have...), add...) {
		named[key] = true
	}
	return canonicalSkips(named)
}

// canonicalSkips renders a named set in canonical order.
func canonicalSkips(named map[string]bool) []string {
	var out []string
	for _, key := range skipKeys {
		if named[key] {
			out = append(out, key)
		}
	}
	return out
}

// ValidSkip reports whether key is one of the canonical skip keys.
func ValidSkip(key string) bool { return slices.Contains(skipKeys, key) }
