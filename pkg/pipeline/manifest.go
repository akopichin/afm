// Package pipeline loads a pipeline package (manifest, agents, flow, skills, scripts) into an immutable
// snapshot and assembles the in-memory Flow and Config a run needs, without touching the user's config files.
package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// ManifestFile is the manifest name at the package root.
	ManifestFile = "pipeline.yaml"
	// ManifestVersion is the only manifest schema_version the runtime accepts.
	ManifestVersion = 2

	maxManifestBytes = 1 << 20
)

var (
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$`)
	rolePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	toolPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
)

// Manifest is pipeline.yaml: what the package contains and what it needs from the environment.
type Manifest struct {
	SchemaVersion int    `yaml:"schema_version" json:"schema_version"`
	Name          string `yaml:"name" json:"name"`
	Version       string `yaml:"version" json:"version"`
	Description   string `yaml:"description,omitempty" json:"description,omitempty"`
	// Entrypoint is the flow file; Agents is the optional agent layer. Both are always part of the snapshot.
	Entrypoint string `yaml:"entrypoint" json:"entrypoint"`
	Agents     string `yaml:"agents,omitempty" json:"agents,omitempty"`
	// Resources are extra files and directories copied into the snapshot.
	Resources    []string        `yaml:"resources,omitempty" json:"resources,omitempty"`
	Roles        map[string]Role `yaml:"roles,omitempty" json:"roles,omitempty"`
	Requirements Requirements    `yaml:"requirements,omitempty" json:"requirements"`
}

// Role binds a stable role ID to the package's command and, optionally, helper profile.
type Role struct {
	Command     string `yaml:"command" json:"command"`
	Profile     string `yaml:"profile,omitempty" json:"profile,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Requirements declares what the package expects from the environment; inspect and preflight report what is missing.
type Requirements struct {
	DockerAutoShim bool     `yaml:"docker_auto_shim,omitempty" json:"docker_auto_shim,omitempty"`
	Collaboration  bool     `yaml:"collaboration,omitempty" json:"collaboration,omitempty"`
	Browser        bool     `yaml:"browser,omitempty" json:"browser,omitempty"`
	Tools          []string `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// LoadManifest reads and validates root/pipeline.yaml. It never follows a symlinked manifest.
func LoadManifest(root string) (Manifest, error) {
	p := filepath.Join(root, ManifestFile)
	info, err := os.Lstat(p)
	if err != nil {
		return Manifest{}, fmt.Errorf("pipeline manifest: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Manifest{}, fmt.Errorf("pipeline manifest %s must be a regular file", ManifestFile)
	}
	if info.Size() > maxManifestBytes {
		return Manifest{}, fmt.Errorf("pipeline manifest %s is larger than %d bytes", ManifestFile, maxManifestBytes)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Manifest{}, fmt.Errorf("pipeline manifest: %w", err)
	}
	return ParseManifest(data)
}

// ParseManifest decodes manifest bytes strictly: unknown keys are errors.
func ParseManifest(data []byte) (Manifest, error) {
	if err := checkSchemaVersion(data); err != nil {
		return Manifest{}, err
	}
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("pipeline manifest %s: %w", ManifestFile, err)
	}
	if err := m.validate(); err != nil {
		return Manifest{}, fmt.Errorf("pipeline manifest %s: %w", ManifestFile, err)
	}
	return m, nil
}

// checkSchemaVersion runs before the strict decode so a catalog (v1) or future manifest gets its own message
// instead of a list of unknown keys.
func checkSchemaVersion(data []byte) error {
	var head struct {
		SchemaVersion *int `yaml:"schema_version"`
	}
	if err := yaml.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("pipeline manifest %s: %w", ManifestFile, err)
	}
	switch v := head.SchemaVersion; {
	case v == nil:
		return fmt.Errorf("pipeline manifest %s: schema_version is required (supported: %d)", ManifestFile, ManifestVersion)
	case *v == 1:
		return fmt.Errorf("pipeline manifest %s has schema_version 1: that is the catalog installer manifest, which afm run --pipeline does not load; use a schema_version %d package or run its flow.yaml directly", ManifestFile, ManifestVersion)
	case *v != ManifestVersion:
		return fmt.Errorf("pipeline manifest %s: unsupported schema_version %d (supported: %d)", ManifestFile, *v, ManifestVersion)
	}
	return nil
}

func (m Manifest) validate() error {
	if !namePattern.MatchString(m.Name) {
		return fmt.Errorf("name %q must match %s", m.Name, namePattern)
	}
	if strings.TrimSpace(m.Version) == "" {
		return errors.New("version is required")
	}
	if err := checkPackagePath("entrypoint", m.Entrypoint); err != nil {
		return err
	}
	if m.Agents != "" {
		if err := checkPackagePath("agents", m.Agents); err != nil {
			return err
		}
	}
	for i, r := range m.Resources {
		if err := checkPackagePath(fmt.Sprintf("resources[%d]", i), r); err != nil {
			return err
		}
	}
	for id, r := range m.Roles {
		if !rolePattern.MatchString(id) {
			return fmt.Errorf("roles.%s: role id must match %s", id, rolePattern)
		}
		if r.Command == "" {
			return fmt.Errorf("roles.%s.command is required", id)
		}
	}
	for i, tool := range m.Requirements.Tools {
		if !toolPattern.MatchString(tool) {
			return fmt.Errorf("requirements.tools[%d]: %q is not a command name", i, tool)
		}
	}
	return nil
}

// checkPackagePath accepts only a clean relative path that stays inside the package root.
func checkPackagePath(field, p string) error {
	if p == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\\' }) {
		return fmt.Errorf("%s %q contains a control or backslash character", field, p)
	}
	if path.IsAbs(p) || filepath.IsAbs(p) {
		return fmt.Errorf("%s %q must be relative to the package root", field, p)
	}
	clean := path.Clean(p)
	if clean == "." || clean == parentDir || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%s %q leaves the package root", field, p)
	}
	if clean != p {
		return fmt.Errorf("%s %q must be written in clean form %q", field, p, clean)
	}
	return nil
}

// ReadLimited reads at most limit bytes and fails on larger input, so a hostile file cannot exhaust memory.
func ReadLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("input is larger than %d bytes", limit)
	}
	return data, nil
}
