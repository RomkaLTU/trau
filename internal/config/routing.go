package config

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// RoutingFingerprint identifies the routing configuration a run executes under.
// Keys holds the resolved routing-relevant values — the active provider, every
// phase's provider/model/effort after layering, and the required skills — and Hash
// is a stable digest over them, so two repos whose effective routing matches share
// a hash no matter which config file supplied it. Nothing else participates, so
// no secret can reach either field.
type RoutingFingerprint struct {
	Hash string
	Keys map[string]string
}

// ResolveRouting fingerprints c's effective routing. A phase with no route entry
// resolves to the active provider's own default model and effort — what the loop
// would actually dispatch it to.
func ResolveRouting(c Config) RoutingFingerprint {
	skills := append([]string(nil), c.RequiredSkills...)
	sort.Strings(skills)

	keys := map[string]string{
		"PROVIDER":        c.Provider,
		"REQUIRED_SKILLS": strings.Join(skills, ","),
	}
	for _, ph := range phases {
		keys["PHASE_"+strings.ToUpper(ph)] = c.effectiveRoute(ph)
	}
	return RoutingFingerprint{Hash: hashRoutingKeys(keys), Keys: keys}
}

// effectiveRoute renders phase's resolved route as provider:model:effort, filling
// each field the route spec left blank from the provider's own default. All three
// fields are always present, so a spec that omits effort and one that sets it
// empty hash identically.
func (c Config) effectiveRoute(phase string) string {
	provider, model, effort := parseRouteSpec(c.Routes[phase])
	if provider == "" {
		provider = c.Provider
	}
	defModel, defEffort := providerRouteDefaults(c, provider)
	if model == "" {
		model = defModel
	}
	if effort == "" {
		effort = defEffort
	}
	return provider + ":" + model + ":" + effort
}

func providerRouteDefaults(c Config, provider string) (model, effort string) {
	switch provider {
	case "claude":
		return c.ClaudeModel, c.ClaudeEffort
	case "codex":
		return c.CodexModel, c.CodexEffort
	case "kimi":
		return c.KimiModel, ""
	}
	return "", ""
}

// hashRoutingKeys digests keys in sorted name order, terminating every name and
// value with a NUL so no two distinct maps can concatenate to the same input.
func hashRoutingKeys(keys map[string]string) string {
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte(0)
		b.WriteString(keys[name])
		b.WriteByte(0)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:16]
}
