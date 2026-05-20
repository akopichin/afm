package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// ClientConfig configures the AI client command.
type ClientConfig struct {
	Command   string   `yaml:"command"`
	ExtraArgs []string `yaml:"extra_args"`
}

// ExecutorConfig controls agent execution parameters.
type ExecutorConfig struct {
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	MaxParallel int           `yaml:"max_parallel"`
}

// ServerConfig configures the web dashboard server.
type ServerConfig struct {
	Port        *int  `yaml:"port"`
	OpenBrowser *bool `yaml:"open_browser"`
}

// IsOpenBrowser returns OpenBrowser value (defaults to true).
func (s ServerConfig) IsOpenBrowser() bool {
	if s.OpenBrowser == nil {
		return true
	}
	return *s.OpenBrowser
}

// GetPort returns Port value (defaults to 9876).
func (s ServerConfig) GetPort() int {
	if s.Port == nil {
		return 9876
	}
	return *s.Port
}

// Config is the merged configuration for flowmanager.
type Config struct {
	Client     ClientConfig   `yaml:"client"`
	Executor   ExecutorConfig `yaml:"executor"`
	Server     ServerConfig   `yaml:"server"`
	PromptsDir string         `yaml:"prompts_dir"`
}

// Default returns the built-in default configuration.
func Default() Config {
	openBrowser := true
	port := 9876
	return Config{
		Client:   ClientConfig{Command: "claude"},
		Executor: ExecutorConfig{IdleTimeout: 30 * time.Minute, MaxParallel: 0},
		Server:   ServerConfig{Port: &port, OpenBrowser: &openBrowser},
	}
}

// Load loads configuration from the standard locations:
// ~/.flowManager/config.yaml (global) and .flowManager/config.yaml (project).
func Load() (Config, error) {
	home, _ := os.UserHomeDir()
	return LoadFrom(
		filepath.Join(home, ".flowManager"),
		".flowManager",
	)
}

// LoadFrom loads and merges config from explicit global and project dirs.
// Missing files are silently ignored; defaults apply.
func LoadFrom(globalDir, projectDir string) (Config, error) {
	cfg := Default()
	if err := mergeFile(&cfg, filepath.Join(globalDir, "config.yaml")); err != nil {
		return cfg, err
	}
	if err := mergeFile(&cfg, filepath.Join(projectDir, "config.yaml")); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func mergeFile(dst *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read config %s: %w", path, err)
	}
	var overlay Config
	if err := yaml.Unmarshal(data, &overlay); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	if overlay.Client.Command != "" {
		dst.Client.Command = overlay.Client.Command
	}
	if overlay.Client.ExtraArgs != nil {
		dst.Client.ExtraArgs = overlay.Client.ExtraArgs
	}
	if overlay.Executor.IdleTimeout != 0 {
		dst.Executor.IdleTimeout = overlay.Executor.IdleTimeout
	}
	if overlay.Executor.MaxParallel != 0 {
		dst.Executor.MaxParallel = overlay.Executor.MaxParallel
	}
	if overlay.PromptsDir != "" {
		dst.PromptsDir = overlay.PromptsDir
	}
	if overlay.Server.Port != nil {
		dst.Server.Port = overlay.Server.Port
	}
	if overlay.Server.OpenBrowser != nil {
		dst.Server.OpenBrowser = overlay.Server.OpenBrowser
	}
	return nil
}
