package manifest

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadManifest reads a YAML plan manifest from the given file path,
// parses it, and validates it. Returns the validated manifest or an error.
func LoadManifest(path string) (*PlanManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %q: %w", path, err)
	}
	return ParseManifest(data)
}

// ParseManifest parses and validates a plan manifest from raw YAML bytes.
// Callers that already have the bytes (e.g. from an embed or test) can use
// this directly without touching the filesystem.
func ParseManifest(data []byte) (*PlanManifest, error) {
	var m PlanManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}
