package state

import "strings"

// SkipsKey is the checkpoint key a run's skip set is recorded under. The set is
// per-run rather than configuration, so the checkpoint is the only place it
// survives: a resume that carries no argv reads its skips back from here (ADR
// 0037). Like every other key it must stay stable across versions.
const SkipsKey = "SKIPS"

// EncodeSkips renders a skip set for the checkpoint. The keys are a fixed
// vocabulary with no comma in them, so a comma-joined list needs no quoting.
func EncodeSkips(keys []string) string { return strings.Join(keys, ",") }

// DecodeSkips reads a checkpoint's recorded skip set back. It validates nothing:
// the flag parser already rejected an unknown key, and a checkpoint written by a
// newer binary must not make an older one fail to resume.
func DecodeSkips(v string) []string {
	var keys []string
	for _, raw := range strings.Split(v, ",") {
		if key := strings.TrimSpace(raw); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}
