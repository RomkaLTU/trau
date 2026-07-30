package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// phaseRoutesPinnedKey marks a config file whose provider defaults have already
// been expanded into explicit per-phase keys. The provider default stays in the
// file afterwards, so without the marker the pass would run again on the next
// startup and resurrect every per-phase key the user has since reset to its
// default.
const phaseRoutesPinnedKey = "PHASE_ROUTES_PINNED"

// upgradeStampName is the file, written under the trau home the first time this
// install runs with fixed per-phase defaults, whose timestamp dates the upgrade.
const upgradeStampName = "phase-routes-upgraded"

// MigratePhaseRoutes preserves the routing of configs written while a provider
// default still cascaded into every phase. Someone who set CLAUDE_MODEL=sonnet ran
// sonnet everywhere; under the fixed per-phase defaults (ADR 0025) their heavy
// phases would silently jump back to opus, so every phase the cascade used to steer
// is pinned explicitly in the same file that carries the provider default. Phases
// whose old value already equals the new default are left alone.
//
// Only a file older than the upgrade stamp can carry the old cascade, and every
// file the pass writes gets PHASE_ROUTES_PINNED in the same write. Together those
// make the pass one-time: a provider default written after the upgrade — say a
// CLAUDE_MODEL picked for grill sessions and the hub agent — is a deliberate
// setting, never a legacy cascade to expand.
//
// A provider default supplied only through the environment has no file to be
// pinned into and is skipped, as is a run with no trau home to date the upgrade
// from.
func MigratePhaseRoutes(projectPath, userPath, localPath, trauHome string) error {
	if trauHome == "" {
		return nil
	}
	upgraded, err := stampUpgrade(trauHome)
	if err != nil {
		return err
	}

	type layer struct {
		path string
		keys map[string]string
	}
	// Highest precedence first, matching LoadLayeredWithSources.
	var layers []layer
	legacy := map[string]bool{}
	for _, path := range []string{projectPath, localPath, userPath} {
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		keys, err := ParseEnvFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		layers = append(layers, layer{path: path, keys: keys})
		legacy[path] = keys[phaseRoutesPinnedKey] == "" && info.ModTime().Before(upgraded)
	}

	carrier := func(key string) (path, value string) {
		for _, l := range layers {
			if v := l.keys[key]; v != "" {
				return l.path, v
			}
		}
		return "", ""
	}
	isSet := func(key string) bool {
		if envOverride(key) != "" {
			return true
		}
		_, v := carrier(key)
		return v != ""
	}

	writes := map[string]map[string]string{}
	add := func(path, key, value string) {
		if writes[path] == nil {
			writes[path] = map[string]string{}
		}
		writes[path][key] = value
	}

	for _, provider := range []string{"claude", "codex"} {
		prefix := strings.ToUpper(provider) + "_"
		modelFile, defaultModel := carrier(prefix + "MODEL")
		effortFile, defaultEffort := carrier(prefix + "EFFORT")
		if !legacy[modelFile] {
			modelFile = ""
		}
		if !legacy[effortFile] {
			effortFile = ""
		}
		for _, ph := range phases {
			phasePrefix := prefix + strings.ToUpper(ph) + "_"
			modelKey, effortKey := phasePrefix+"MODEL", phasePrefix+"EFFORT"
			modelSet, effortSet := isSet(modelKey), isSet(effortKey)
			newModel, newEffort := phaseRouteDefault(provider, ph, "")

			if modelFile != "" && !modelSet {
				old := defaultModel
				// The old seed only applied to a phase left entirely unconfigured; an
				// explicit effort alone put the phase back on the provider default.
				if seed := legacySeededModel(provider, ph); seed != "" && !effortSet {
					old = seed
				}
				if old != newModel {
					add(modelFile, modelKey, old)
				}
			}
			if effortFile != "" && !effortSet && defaultEffort != newEffort {
				add(effortFile, effortKey, defaultEffort)
			}
		}
	}

	for path, values := range writes {
		values[phaseRoutesPinnedKey] = "1"
		if err := WriteEnvFile(path, values); err != nil {
			return fmt.Errorf("pin per-phase routing in %s: %w", path, err)
		}
	}
	return nil
}

// stampUpgrade records, once per trau home, when this install first ran with fixed
// per-phase defaults, and returns that moment. A config file written after it was
// authored under the fixed defaults, so it has no cascade left to preserve.
func stampUpgrade(trauHome string) (time.Time, error) {
	path := filepath.Join(trauHome, upgradeStampName)
	info, err := os.Stat(path)
	if err == nil {
		return info.ModTime(), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return time.Time{}, fmt.Errorf("read %s: %w", path, err)
	}
	if err := os.MkdirAll(trauHome, 0o755); err != nil {
		return time.Time{}, fmt.Errorf("create %s: %w", trauHome, err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		return time.Time{}, fmt.Errorf("write %s: %w", path, err)
	}
	return time.Now(), nil
}

// legacySeededModel is the pre-ADR-0025 seed table: the only phases whose model did
// not come straight from the provider default. It survives here, and nowhere else,
// so MigratePhaseRoutes can reconstruct what an old config used to resolve to.
func legacySeededModel(provider, phase string) string {
	if provider != "claude" {
		return ""
	}
	switch phase {
	case "cleanup", "commit", "handoff":
		return "sonnet"
	case "lintfix":
		return "haiku"
	}
	return ""
}
