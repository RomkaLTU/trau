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

// CanonicalSkips reads an already-split skip set — a JSON array from the hub, a
// picker's answer — into the canonical keys it names. It rejects an unknown key
// naming the valid set, so a typo fails at the edge rather than silently running
// the work the operator meant to bypass. Blanks drop, repeats collapse, and the
// result keeps canonical order, so the same choice always records the same set;
// an empty set returns nil.
func CanonicalSkips(keys []string) ([]string, error) {
	named := map[string]bool{}
	for _, raw := range keys {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" {
			continue
		}
		if !ValidSkip(key) {
			return nil, fmt.Errorf("unknown key %q; valid keys are %s", raw, strings.Join(skipKeys, ", "))
		}
		named[key] = true
	}
	return canonicalSkips(named), nil
}

// ParseSkips reads one comma-separated --skip value through the same vocabulary
// the hub validates against, so the flag and the queue cannot disagree about
// what a key means.
func ParseSkips(v string) ([]string, error) {
	keys, err := CanonicalSkips(strings.Split(v, ","))
	if err != nil {
		return nil, fmt.Errorf("--skip: %w", err)
	}
	return keys, nil
}

// MergeSkips folds a second skip set into the one an earlier gesture produced —
// a repeated --skip occurrence, or a launch-time choice landing on the set an
// item was queued with — keeping the canonical order and dropping the repeats.
func MergeSkips(have, add []string) []string {
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
