package config

import (
	"os"
	"path/filepath"
	"sort"
)

// LayerPaths names the config file each file layer resolved to. An empty path
// means the layer has no file for this run.
type LayerPaths struct {
	Project string
	Local   string
	User    string
}

// LayerFile is one file layer's resolved location.
type LayerFile struct {
	Layer  Layer
	Path   string
	Exists bool
}

// Files returns the three file layers highest precedence first, each path made
// absolute so a stray file can be identified from the report alone.
func (p LayerPaths) Files() []LayerFile {
	files := []LayerFile{
		{Layer: LayerProject, Path: p.Project},
		{Layer: LayerLocal, Path: p.Local},
		{Layer: LayerUser, Path: p.User},
	}
	for i, f := range files {
		if f.Path == "" {
			continue
		}
		if abs, err := filepath.Abs(f.Path); err == nil {
			files[i].Path = abs
		}
		_, err := os.Stat(files[i].Path)
		files[i].Exists = err == nil
	}
	return files
}

// ShadowedKey is one key a higher file layer sets to an empty value while a
// lower layer sets it to a real one. An empty value is an explicit unset that
// wins on precedence, so the lower layer's value never takes effect.
type ShadowedKey struct {
	Key      string
	By       Layer
	ByPath   string
	Over     Layer
	OverPath string
	// Value is what the lower layer holds, redacted for credential keys.
	Value string
}

// RedactedValue stands in for a credential a report must not print.
const RedactedValue = "(redacted)"

// ShadowedKeys reports every key blanked out by a higher config layer, ordered
// by key. A key an environment variable resolves is skipped: env outranks every
// file, so the blank line no longer decides the value.
func ShadowedKeys(p LayerPaths) []ShadowedKey {
	type layerContent struct {
		LayerFile
		keys map[string]string
	}

	files := p.Files()
	layers := make([]layerContent, 0, len(files))
	for _, f := range files {
		keys, err := ParseEnvFile(f.Path)
		if err != nil {
			continue
		}
		layers = append(layers, layerContent{LayerFile: f, keys: keys})
	}

	var out []ShadowedKey
	claimed := map[string]bool{}
	for i, top := range layers {
		for key, value := range top.keys {
			if claimed[key] {
				continue
			}
			claimed[key] = true
			if value != "" || envOverride(key) != "" {
				continue
			}
			for _, lower := range layers[i+1:] {
				v := lower.keys[key]
				if v == "" {
					continue
				}
				if IsSecretKey(key) {
					v = RedactedValue
				}
				out = append(out, ShadowedKey{
					Key:      key,
					By:       top.Layer,
					ByPath:   top.Path,
					Over:     lower.Layer,
					OverPath: lower.Path,
					Value:    v,
				})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
