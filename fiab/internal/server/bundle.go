package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Bundle is the manifest written by mkBundle (bundle.json).
type Bundle struct {
	Version     int    `json:"version"`
	Name        string `json:"name"`
	Description string `json:"description"`
	DefaultMode string `json:"defaultMode"`
	Modes       []Mode `json:"modes"`
	// Caches lists cache directories (relative to the bundle) that are
	// served merged under /cache/.
	Caches []string `json:"caches"`
}

// Mode is one dist flavour of the bundle.
type Mode struct {
	Name string `json:"name"`
	// Kind is "lib" (main flake exports lib.mkOutputs { cacheUrl }) or
	// "passthrough" (wrapper re-exports the main flake's outputs).
	Kind         string `json:"kind"`
	Dist         string `json:"dist"`
	NarHash      string `json:"narHash"`
	LastModified int64  `json:"lastModified"`
	// Outputs are the output names re-exported by passthrough wrappers.
	Outputs []string `json:"outputs"`
	// ExperimentalFeatures are added to the wrapper's nixConfig.
	ExperimentalFeatures []string `json:"experimentalFeatures"`
	// Signed reports whether consumers need the signing key trusted.
	Signed bool `json:"signed"`
	// NeedsFetchClosure reports whether consumers must enable the
	// fetch-closure experimental feature themselves.
	NeedsFetchClosure bool `json:"needsFetchClosure"`
	// Lock is the main flake's own flake.lock, if any.
	Lock json.RawMessage `json:"lock,omitempty"`
}

var modeNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// LoadBundle reads and validates bundle.json.
func LoadBundle(dir string) (*Bundle, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		return nil, err
	}
	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("bundle.json: %w", err)
	}
	if b.Version != 1 {
		return nil, fmt.Errorf("bundle.json: unsupported version %d", b.Version)
	}
	if len(b.Modes) == 0 {
		return nil, fmt.Errorf("bundle.json: no modes")
	}
	seen := map[string]bool{}
	for _, m := range b.Modes {
		if !modeNameRE.MatchString(m.Name) || m.Name == "flake" || m.Name == "dist" || m.Name == "cache" || m.Name == "immutable" {
			return nil, fmt.Errorf("bundle.json: invalid mode name %q", m.Name)
		}
		if seen[m.Name] {
			return nil, fmt.Errorf("bundle.json: duplicate mode %q", m.Name)
		}
		seen[m.Name] = true
		if m.Kind != "lib" && m.Kind != "passthrough" {
			return nil, fmt.Errorf("bundle.json: mode %q has unknown kind %q", m.Name, m.Kind)
		}
		if _, err := os.Stat(filepath.Join(dir, m.Dist)); err != nil {
			return nil, fmt.Errorf("bundle.json: mode %q: %w", m.Name, err)
		}
	}
	if b.DefaultMode == "" {
		b.DefaultMode = b.Modes[0].Name
	}
	if !seen[b.DefaultMode] {
		return nil, fmt.Errorf("bundle.json: default mode %q not defined", b.DefaultMode)
	}
	return &b, nil
}

// Mode returns the named mode.
func (b *Bundle) Mode(name string) *Mode {
	for i := range b.Modes {
		if b.Modes[i].Name == name {
			return &b.Modes[i]
		}
	}
	return nil
}
