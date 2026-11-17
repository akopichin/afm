// Package serve is the host side of `afm serve`: one persistent MCP endpoint over the afm runs of this machine.
package serve

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultMaxRunning and DefaultMaxStarting apply when serve.yaml does not set the limits.
	DefaultMaxRunning  = 4
	DefaultMaxStarting = 2
)

var idPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// Config is ~/.afm/serve.yaml. Only listed projects and pipelines can be started, so a start request never
// becomes an arbitrary command.
type Config struct {
	Projects  []Project  `yaml:"projects,omitempty"`
	Pipelines []Pipeline `yaml:"pipelines,omitempty"`
	Limits    Limits     `yaml:"limits"`
	Retention Retention  `yaml:"retention,omitempty"`
}

// Project is a directory a run may be started in.
type Project struct {
	ID   string `yaml:"id"`
	Path string `yaml:"path"`
}

// Pipeline is a pipeline package a run may be started from; Options is an optional options file.
type Pipeline struct {
	ID      string `yaml:"id"`
	Source  string `yaml:"source"`
	Options string `yaml:"options,omitempty"`
}

// Limits bound concurrent runs.
type Limits struct {
	MaxRunning  int `yaml:"max_running"`
	MaxStarting int `yaml:"max_starting"`
}

// Retention controls when the host releases a finished run's runtime; zero disables it.
type Retention struct {
	ReleaseAfter time.Duration `yaml:"release_after,omitempty"`
}

// Project returns the allowlisted project with the given id.
func (c Config) Project(id string) (Project, bool) {
	for _, p := range c.Projects {
		if p.ID == id {
			return p, true
		}
	}
	return Project{}, false
}

// Pipeline returns the allowlisted pipeline with the given id.
func (c Config) Pipeline(id string) (Pipeline, bool) {
	for _, p := range c.Pipelines {
		if p.ID == id {
			return p, true
		}
	}
	return Pipeline{}, false
}

// LoadConfig reads serve.yaml; a missing file is an empty config with default limits.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ParseConfig(nil)
	}
	if err != nil {
		return Config{}, fmt.Errorf("serve config: %w", err)
	}
	return ParseConfig(data)
}

// ParseConfig decodes serve.yaml strictly: unknown keys are errors.
func ParseConfig(data []byte) (Config, error) {
	// Defaults are set before decoding so an explicit 0 is rejected instead of silently replaced.
	cfg := Config{Limits: Limits{MaxRunning: DefaultMaxRunning, MaxStarting: DefaultMaxStarting}}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("serve config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("serve config: %w", err)
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if err := c.Limits.validate(); err != nil {
		return err
	}
	if c.Retention.ReleaseAfter < 0 {
		return errors.New("retention.release_after must not be negative")
	}
	seen := map[string]bool{}
	for i, p := range c.Projects {
		if err := checkID("projects", i, p.ID, seen); err != nil {
			return err
		}
		if err := requireExisting(fmt.Sprintf("projects[%d].path", i), p.Path, true); err != nil {
			return err
		}
	}
	seen = map[string]bool{}
	for i, p := range c.Pipelines {
		if err := checkID("pipelines", i, p.ID, seen); err != nil {
			return err
		}
		if p.Source == "" {
			return fmt.Errorf("pipelines[%d].source is required", i)
		}
		if p.Options == "" {
			continue
		}
		if err := requireExisting(fmt.Sprintf("pipelines[%d].options", i), p.Options, false); err != nil {
			return err
		}
	}
	return nil
}

func (l Limits) validate() error {
	if l.MaxRunning < 1 {
		return fmt.Errorf("limits.max_running must be at least 1, got %d", l.MaxRunning)
	}
	if l.MaxStarting < 1 {
		return fmt.Errorf("limits.max_starting must be at least 1, got %d", l.MaxStarting)
	}
	return nil
}

func checkID(list string, i int, id string, seen map[string]bool) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%s[%d].id %q must match %s", list, i, id, idPattern)
	}
	if seen[id] {
		return fmt.Errorf("%s: duplicate id %q", list, id)
	}
	seen[id] = true
	return nil
}

func requireExisting(field, path string, wantDir bool) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be an absolute path, got %q", field, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if info.IsDir() != wantDir {
		kind := "a regular file"
		if wantDir {
			kind = "a directory"
		}
		return fmt.Errorf("%s %q must be %s", field, path, kind)
	}
	return nil
}
