package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
)

func writeYAML(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

const defaultCommand = "claude"

func TestDefaultConfig(t *testing.T) {
	cfg := config.Default()
	if cfg.Client.Command != defaultCommand {
		t.Errorf("default command: got %q want %q", cfg.Client.Command, "claude")
	}
	if cfg.Executor.IdleTimeout != 30*time.Minute {
		t.Errorf("default idle timeout: got %v", cfg.Executor.IdleTimeout)
	}
	if cfg.Executor.MaxParallel != 0 {
		t.Errorf("default max_parallel: got %d", cfg.Executor.MaxParallel)
	}
}

func TestLoadProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	writeYAML(t, globalDir, "config.yaml", `
client:
  command: claude
executor:
  idle_timeout: 10m
`)
	writeYAML(t, projectDir, "config.yaml", `
client:
  command: gemini
`)

	cfg, err := config.LoadFrom(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Client.Command != "gemini" {
		t.Errorf("project should override command: got %q", cfg.Client.Command)
	}
	if cfg.Executor.IdleTimeout != 10*time.Minute {
		t.Errorf("global idle_timeout should carry over: got %v", cfg.Executor.IdleTimeout)
	}
}

func TestLoadMissingFiles(t *testing.T) {
	cfg, err := config.LoadFrom("/nonexistent", "/also/nonexistent")
	if err != nil {
		t.Fatalf("missing config files should not error: %v", err)
	}
	if cfg.Client.Command != defaultCommand {
		t.Errorf("should fall back to defaults: got %q", cfg.Client.Command)
	}
}

func TestServerConfigDefaults(t *testing.T) {
	cfg := config.Default()
	if cfg.Server.GetPort() != 9876 {
		t.Errorf("default port: got %d, want 9876", cfg.Server.GetPort())
	}
	if !cfg.Server.IsOpenBrowser() {
		t.Error("default open_browser should be true")
	}
}

func TestServerConfigOverride(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "server:\n  port: 8080\n  open_browser: false\n")

	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.GetPort() != 8080 {
		t.Errorf("port: got %d, want 8080", cfg.Server.GetPort())
	}
	if cfg.Server.IsOpenBrowser() {
		t.Error("open_browser should be false")
	}
}

func TestServerPortZeroDisablesServer(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "server:\n  port: 0\n")

	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.GetPort() != 0 {
		t.Errorf("port should be 0 when explicitly set: got %d", cfg.Server.GetPort())
	}
}

func TestProxyConfig_IsEnabled(t *testing.T) {
	var p config.ProxyConfig
	if !p.IsEnabled() {
		t.Error("nil Enabled should default to true")
	}
	f := false
	p.Enabled = &f
	if p.IsEnabled() {
		t.Error("Enabled=false should return false")
	}
	tr := true
	p.Enabled = &tr
	if !p.IsEnabled() {
		t.Error("Enabled=true should return true")
	}
}

func TestProxyConfigMerge(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", `
proxy:
  enabled: false
  upstream: https://api.z.ai/api/anthropic
  port: 9000
  transforms:
    zai: true
`)
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Proxy.IsEnabled() {
		t.Error("proxy.enabled=false should disable proxy")
	}
	if cfg.Proxy.Upstream != "https://api.z.ai/api/anthropic" {
		t.Errorf("upstream: got %q", cfg.Proxy.Upstream)
	}
	if cfg.Proxy.Port != 9000 {
		t.Errorf("port: got %d, want 9000", cfg.Proxy.Port)
	}
	if cfg.Proxy.Transforms.ZAI == nil || !*cfg.Proxy.Transforms.ZAI {
		t.Error("transforms.zai should be true")
	}
}

func TestProxyConfigMergeDefaults(t *testing.T) {
	cfg := config.Default()
	if !cfg.Proxy.IsEnabled() {
		t.Error("proxy should be enabled by default")
	}
	if cfg.Proxy.Upstream != "" {
		t.Errorf("default upstream should be empty, got %q", cfg.Proxy.Upstream)
	}
	if cfg.Proxy.Port != 0 {
		t.Errorf("default port should be 0, got %d", cfg.Proxy.Port)
	}
}

func TestDockerConfig_IsDockerEnabled(t *testing.T) {
	trueVal := true
	falseVal := false

	cases := []struct {
		name         string
		cfg          config.DockerConfig
		envUseDocker string
		envInDocker  string
		want         bool
	}{
		{"enabled=true", config.DockerConfig{Enabled: &trueVal}, "", "", true},
		{"enabled=false", config.DockerConfig{Enabled: &falseVal}, "", "", false},
		{"nil+env=1", config.DockerConfig{}, "1", "", true},
		{"nil+env=true", config.DockerConfig{}, "true", "", true},
		{"nil+env=", config.DockerConfig{}, "", "", false},
		{"in_docker overrides", config.DockerConfig{Enabled: &trueVal}, "", "1", false},
		{"explicit=true wins over AFM_IN_DOCKER=1 not", config.DockerConfig{Enabled: &trueVal}, "", "1", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AFM_USE_DOCKER", tc.envUseDocker)
			t.Setenv("AFM_IN_DOCKER", tc.envInDocker)
			if got := tc.cfg.IsDockerEnabled(); got != tc.want {
				t.Errorf("IsDockerEnabled()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestDockerConfig_GetImage(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("AFM_DOCKER_IMAGE", "")
		cfg := config.DockerConfig{}
		if cfg.GetImage() != "akopichin/afm:latest" {
			t.Errorf("got %q, want akopichin/afm:latest", cfg.GetImage())
		}
	})
	t.Run("config override", func(t *testing.T) {
		t.Setenv("AFM_DOCKER_IMAGE", "")
		cfg := config.DockerConfig{Image: "myrepo/afm:v1"}
		if cfg.GetImage() != "myrepo/afm:v1" {
			t.Errorf("got %q", cfg.GetImage())
		}
	})
	t.Run("env override", func(t *testing.T) {
		t.Setenv("AFM_DOCKER_IMAGE", "local/afm:dev")
		cfg := config.DockerConfig{Image: "myrepo/afm:v1"}
		if cfg.GetImage() != "local/afm:dev" {
			t.Errorf("got %q", cfg.GetImage())
		}
	})
}

// TestConfigPricingAccountingFieldsExists is a contract test: Config must expose
// Pricing (PricingConfig) and Accounting (AccountingConfig) fields, and the new
// sub-config types must carry their declared properties + getter methods.
func TestConfigPricingAccountingFieldsExists(t *testing.T) {
	var cfg config.Config

	// Config fields exist and are assignable to the declared types.
	cfg.Pricing = config.PricingConfig{
		Models: map[string]config.ModelPricing{
			"claude-sonnet-5": {
				InputPerMtok: 3.0, OutputPerMtok: 15.0, CachePerMtok: 0.3,
			},
		},
	}
	cfg.Accounting = config.AccountingConfig{BucketMinutes: 5}

	// PricingConfig.GetModelPricing signature: (string) (ModelPricing, bool).
	pricing, ok := cfg.Pricing.GetModelPricing("claude-sonnet-5")
	if !ok {
		t.Fatal("GetModelPricing for configured model must return ok=true")
	}
	_ = pricing

	// AccountingConfig.GetBucketMinutes signature: () int.
	if minutes := cfg.Accounting.GetBucketMinutes(); minutes != 5 {
		t.Errorf("GetBucketMinutes: got %d, want 5", minutes)
	}
}

func TestPricingConfig_GetModelPricingFound(t *testing.T) {
	p := config.PricingConfig{
		Models: map[string]config.ModelPricing{
			"claude-sonnet-5": {InputPerMtok: 3.0, OutputPerMtok: 15.0, CachePerMtok: 0.3},
		},
	}
	got, ok := p.GetModelPricing("claude-sonnet-5")
	if !ok {
		t.Fatal("expected ok=true for a configured model")
	}
	want := config.ModelPricing{InputPerMtok: 3.0, OutputPerMtok: 15.0, CachePerMtok: 0.3}
	if got != want {
		t.Errorf("GetModelPricing: got %+v, want %+v", got, want)
	}
}

func TestPricingConfig_GetModelPricingUnknownModel(t *testing.T) {
	p := config.PricingConfig{
		Models: map[string]config.ModelPricing{
			"claude-sonnet-5": {InputPerMtok: 3.0, OutputPerMtok: 15.0, CachePerMtok: 0.3},
		},
	}
	got, ok := p.GetModelPricing("claude-opus-4")
	if ok {
		t.Error("expected ok=false for a model not in the table (no fuzzy fallback)")
	}
	if got != (config.ModelPricing{}) {
		t.Errorf("expected zero-value ModelPricing for unknown model, got %+v", got)
	}
}

func TestPricingConfig_GetModelPricingNilModelsMap(t *testing.T) {
	// Zero-value PricingConfig — Models is nil. The lookup must be ok=false and
	// never panic on the nil-map read.
	var p config.PricingConfig
	got, ok := p.GetModelPricing("claude-sonnet-5")
	if ok {
		t.Error("expected ok=false when Models is nil")
	}
	if got != (config.ModelPricing{}) {
		t.Errorf("expected zero-value ModelPricing, got %+v", got)
	}
}

func TestAccountingConfig_GetBucketMinutes(t *testing.T) {
	if got := (config.AccountingConfig{}).GetBucketMinutes(); got != 5 {
		t.Errorf("zero value: got %d, want default 5", got)
	}
	if got := (config.AccountingConfig{BucketMinutes: 10}).GetBucketMinutes(); got != 10 {
		t.Errorf("explicit value: got %d, want 10", got)
	}
}

func TestLoadFrom_PricingAccountingOverride(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", `
pricing:
  models:
    claude-sonnet-5:
      input_per_mtok: 3.0
      output_per_mtok: 15.0
      cache_per_mtok: 0.3
accounting:
  bucket_minutes: 10
`)
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	pricing, ok := cfg.Pricing.GetModelPricing("claude-sonnet-5")
	if !ok {
		t.Fatal("pricing override: expected configured model to be found")
	}
	if want := (config.ModelPricing{InputPerMtok: 3.0, OutputPerMtok: 15.0, CachePerMtok: 0.3}); pricing != want {
		t.Errorf("pricing override: got %+v, want %+v", pricing, want)
	}
	if got := cfg.Accounting.GetBucketMinutes(); got != 10 {
		t.Errorf("accounting override: got %d, want 10", got)
	}
}

func TestLoadFrom_PricingAccountingDefaults(t *testing.T) {
	// With no pricing:/accounting: section, Default() yields a zero-value
	// Pricing/Accounting — GetModelPricing is ok=false (cost metric hidden),
	// GetBucketMinutes falls back to 5.
	cfg := config.Default()
	if _, ok := cfg.Pricing.GetModelPricing("claude-sonnet-5"); ok {
		t.Error("default Pricing should be empty (ok=false for every lookup)")
	}
	if got := cfg.Accounting.GetBucketMinutes(); got != 5 {
		t.Errorf("default bucket minutes: got %d, want 5", got)
	}
}

func TestLoadFrom_DockerConfig(t *testing.T) {
	dir := t.TempDir()
	trueVal := true
	writeYAML(t, dir, "config.yaml", `
docker:
  enabled: true
  image: test/afm:dev
  extra_mounts:
    - ~/.ai-free
    - /etc/extra
`)
	cfg, err := config.LoadFrom(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Docker.Enabled == nil || *cfg.Docker.Enabled != trueVal {
		t.Errorf("Docker.Enabled: got %v, want &true", cfg.Docker.Enabled)
	}
	if cfg.Docker.Image != "test/afm:dev" {
		t.Errorf("Docker.Image: got %q", cfg.Docker.Image)
	}
	wantMounts := []string{"~/.ai-free", "/etc/extra"}
	if len(cfg.Docker.ExtraMounts) != len(wantMounts) {
		t.Fatalf("Docker.ExtraMounts: got %v, want %v", cfg.Docker.ExtraMounts, wantMounts)
	}
	for i, w := range wantMounts {
		if cfg.Docker.ExtraMounts[i] != w {
			t.Errorf("Docker.ExtraMounts[%d]: got %q, want %q", i, cfg.Docker.ExtraMounts[i], w)
		}
	}
	_ = trueVal
}

func TestThemeMerge(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "theme: goga\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "goga" {
		t.Errorf("theme: got %q, want %q", cfg.Theme, "goga")
	}
}

func TestThemeEmptyDoesNotOverride(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "theme: \"\"\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "" {
		t.Errorf("empty theme should stay empty: got %q", cfg.Theme)
	}
}

func TestEffectiveTheme(t *testing.T) {
	cases := []struct {
		name  string
		theme string
		want  string
	}{
		{"empty", "", "novacorps"},
		{"goga", "goga", "goga"},
		{"goga-upper", "GOGA", "goga"},
		{"goga-spaced", "  goga  ", "goga"},
		{"novacorps", "novacorps", "novacorps"},
		{"unknown", "dark", "novacorps"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Theme: tc.theme}
			if got := cfg.EffectiveTheme(); got != tc.want {
				t.Errorf("EffectiveTheme(%q)=%q, want %q", tc.theme, got, tc.want)
			}
		})
	}
}
