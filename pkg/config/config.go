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

// TransformOverrides controls which proxy transforms are applied.
// nil = auto-detect by upstream host, true = always, false = never.
type TransformOverrides struct {
	ZAI *bool `yaml:"zai"`
}

// ProxyConfig configures the built-in reverse proxy.
type ProxyConfig struct {
	Enabled    *bool              `yaml:"enabled"`
	Upstream   string             `yaml:"upstream"`
	Port       int                `yaml:"port"`
	Transforms TransformOverrides `yaml:"transforms"`
}

// IsEnabled returns true by default (nil Enabled → enabled).
func (p ProxyConfig) IsEnabled() bool {
	return p.Enabled == nil || *p.Enabled
}

// DockerConfig configures Docker-mode self-re-exec.
type DockerConfig struct {
	Enabled *bool  `yaml:"enabled"` // nil = смотрим AFM_USE_DOCKER
	Image   string `yaml:"image"`
	// ExtraMounts — дополнительные хост-пути (можно с ~), которые пробрасываются
	// в контейнер по тому же пути только для чтения. Нужно кастомным агентам,
	// хранящим токены/конфиги вне ~/.claude (напр. ~/.ai-free).
	ExtraMounts []string `yaml:"extra_mounts"`
}

// IsDockerEnabled returns true if Docker mode should be used.
// AFM_IN_DOCKER=1 always returns false (already inside container).
func (d DockerConfig) IsDockerEnabled() bool {
	if os.Getenv("AFM_IN_DOCKER") == "1" {
		return false
	}
	if d.Enabled != nil {
		return *d.Enabled
	}
	v := os.Getenv("AFM_USE_DOCKER")
	return v == "1" || v == "true"
}

// GetImage returns the Docker image to use, preferring AFM_DOCKER_IMAGE env var.
func (d DockerConfig) GetImage() string {
	if img := os.Getenv("AFM_DOCKER_IMAGE"); img != "" {
		return img
	}
	if d.Image != "" {
		return d.Image
	}
	return "akopichin/afm:latest"
}

// ModelPricing holds a single model's consumption prices in USD per million tokens.
type ModelPricing struct {
	InputPerMtok  float64 `yaml:"input_per_mtok"`
	OutputPerMtok float64 `yaml:"output_per_mtok"`
	CachePerMtok  float64 `yaml:"cache_per_mtok"`
}

// PricingConfig is the optional per-model pricing table. A nil/empty Models map
// means pricing is unconfigured — callers treat the cost metric as fully hidden.
type PricingConfig struct {
	Models map[string]ModelPricing `yaml:"models"`
}

// GetModelPricing returns the pricing for an exact model name.
// Unknown models (or an unconfigured pricing table) yield ok=false — there is
// no fuzzy/prefix fallback. A nil map read is safe in Go and never panics.
func (p PricingConfig) GetModelPricing(model string) (ModelPricing, bool) {
	pricing, ok := p.Models[model]
	return pricing, ok
}

// AccountingConfig controls consumption time-aggregation (bucket width).
type AccountingConfig struct {
	BucketMinutes int `yaml:"bucket_minutes"`
}

// GetBucketMinutes returns BucketMinutes, or 5 when it is 0. A plain int field
// (not *int) is used by design: the zero value doubles as "unset" since a real
// 0-minute bucket width would be meaningless.
func (a AccountingConfig) GetBucketMinutes() int {
	if a.BucketMinutes == 0 {
		return 5
	}
	return a.BucketMinutes
}

// Config is the merged configuration for afm.
type Config struct {
	Client     ClientConfig     `yaml:"client"`
	Executor   ExecutorConfig   `yaml:"executor"`
	Server     ServerConfig     `yaml:"server"`
	Proxy      ProxyConfig      `yaml:"proxy"`
	Docker     DockerConfig     `yaml:"docker"`
	Pricing    PricingConfig    `yaml:"pricing"`
	Accounting AccountingConfig `yaml:"accounting"`
	PromptsDir string           `yaml:"prompts_dir"`
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
	if overlay.Proxy.Enabled != nil {
		dst.Proxy.Enabled = overlay.Proxy.Enabled
	}
	if overlay.Proxy.Upstream != "" {
		dst.Proxy.Upstream = overlay.Proxy.Upstream
	}
	if overlay.Proxy.Port != 0 {
		dst.Proxy.Port = overlay.Proxy.Port
	}
	if overlay.Proxy.Transforms.ZAI != nil {
		dst.Proxy.Transforms.ZAI = overlay.Proxy.Transforms.ZAI
	}
	if overlay.Docker.Enabled != nil {
		dst.Docker.Enabled = overlay.Docker.Enabled
	}
	if overlay.Docker.Image != "" {
		dst.Docker.Image = overlay.Docker.Image
	}
	if overlay.Docker.ExtraMounts != nil {
		dst.Docker.ExtraMounts = overlay.Docker.ExtraMounts
	}
	if overlay.Pricing.Models != nil {
		dst.Pricing.Models = overlay.Pricing.Models
	}
	if overlay.Accounting.BucketMinutes != 0 {
		dst.Accounting.BucketMinutes = overlay.Accounting.BucketMinutes
	}
	return nil
}
