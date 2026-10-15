package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
)

// --- T1.1: tri-state enabled + merge ---------------------------------------------------------------------

func TestCollaboration_DefaultEnabled(t *testing.T) {
	if !(config.CollaborationConfig{}).IsEnabled() {
		t.Fatal("empty config should default to enabled")
	}
}

func TestCollaboration_ConfigFalse(t *testing.T) {
	f := false
	if (config.CollaborationConfig{Enabled: &f}).IsEnabled() {
		t.Fatal("explicit false must disable")
	}
}

func TestCollaboration_EnvZeroOverridesConfigTrue(t *testing.T) {
	t.Setenv("AFM_COLLABORATION", "0")
	tr := true
	if (config.CollaborationConfig{Enabled: &tr}).IsEnabled() {
		t.Fatal("AFM_COLLABORATION=0 must override config enabled:true")
	}
}

func TestCollaboration_EnvOneOverridesConfigFalse(t *testing.T) {
	t.Setenv("AFM_COLLABORATION", "1")
	f := false
	if !(config.CollaborationConfig{Enabled: &f}).IsEnabled() {
		t.Fatal("AFM_COLLABORATION=1 must override config enabled:false")
	}
}

func TestCollaboration_EmptyEnvDoesNotOverride(t *testing.T) {
	t.Setenv("AFM_COLLABORATION", "")
	f := false
	if (config.CollaborationConfig{Enabled: &f}).IsEnabled() {
		t.Fatal("AFM_COLLABORATION=\"\" must not override config enabled:false (envBool treats empty as unset)")
	}
}

func TestMergeFile_ProjectOverridesGlobalEnabled(t *testing.T) {
	dir := t.TempDir()
	g := filepath.Join(dir, "g")
	p := filepath.Join(dir, "p")
	if err := os.MkdirAll(g, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	writeYAML(t, g, "config.yaml", "collaboration:\n  enabled: false\n")
	writeYAML(t, p, "config.yaml", "collaboration:\n  enabled: true\n")
	cfg, err := config.LoadFrom(g, p)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Collaboration.IsEnabled() {
		t.Error("project enabled:true must override global enabled:false")
	}
}

func TestMergeFile_ProjectNilKeepsGlobal(t *testing.T) {
	dir := t.TempDir()
	g := filepath.Join(dir, "g")
	p := filepath.Join(dir, "p")
	if err := os.MkdirAll(g, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	writeYAML(t, g, "config.yaml", "collaboration:\n  enabled: false\n")
	writeYAML(t, p, "config.yaml", "theme: graphite\n") // project layer silent on collaboration
	cfg, err := config.LoadFrom(g, p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collaboration.IsEnabled() {
		t.Error("project config absent of collaboration must not erase global enabled:false")
	}
}

// --- T1.2: collaboration.max_depth in global/project config -----------------------------------------------

func TestMaxDepth_UnsetIsNil(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "theme: graphite\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collaboration.MaxDepth != nil {
		t.Errorf("MaxDepth = %v, want nil when the key is absent", *cfg.Collaboration.MaxDepth)
	}
}

func TestMaxDepth_ExplicitZeroPreservedThroughMerge(t *testing.T) {
	dir := t.TempDir()
	g := filepath.Join(dir, "g")
	p := filepath.Join(dir, "p")
	if err := os.MkdirAll(g, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	writeYAML(t, g, "config.yaml", "collaboration:\n  max_depth: 3\n")
	writeYAML(t, p, "config.yaml", "collaboration:\n  max_depth: 0\n")
	cfg, err := config.LoadFrom(g, p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collaboration.MaxDepth == nil || *cfg.Collaboration.MaxDepth != 0 {
		t.Fatalf("MaxDepth = %v, want explicit 0 (project overrides global 3)", cfg.Collaboration.MaxDepth)
	}
}

func TestMaxDepth_NegativeRejectedAtLoad(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "collaboration:\n  max_depth: -1\n")
	_, err := config.LoadFrom("", dir)
	if err == nil {
		t.Fatal("expected LoadFrom to reject a negative collaboration.max_depth")
	}
	if !strings.Contains(err.Error(), "collaboration.max_depth") {
		t.Errorf("error = %q, want it to name collaboration.max_depth", err.Error())
	}
	if !strings.Contains(err.Error(), "project") {
		t.Errorf("error = %q, want it to name the project layer", err.Error())
	}
}

func TestMaxDepth_NegativeRejectedAtLoad_NamesGlobalLayer(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "collaboration:\n  max_depth: -1\n")
	_, err := config.LoadFrom(dir, "")
	if err == nil {
		t.Fatal("expected LoadFrom to reject a negative collaboration.max_depth")
	}
	if !strings.Contains(err.Error(), "global") {
		t.Errorf("error = %q, want it to name the global layer", err.Error())
	}
}

func TestMaxDepth_ProjectOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	g := filepath.Join(dir, "g")
	p := filepath.Join(dir, "p")
	if err := os.MkdirAll(g, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	writeYAML(t, g, "config.yaml", "collaboration:\n  max_depth: 3\n")
	writeYAML(t, p, "config.yaml", "collaboration:\n  max_depth: 5\n")
	cfg, err := config.LoadFrom(g, p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collaboration.MaxDepth == nil || *cfg.Collaboration.MaxDepth != 5 {
		t.Fatalf("MaxDepth = %v, want 5 (project overrides global)", cfg.Collaboration.MaxDepth)
	}
	global, project := cfg.Collaboration.MaxDepthLayers()
	if global == nil || *global != 3 {
		t.Errorf("MaxDepthLayers() global = %v, want 3", global)
	}
	if project == nil || *project != 5 {
		t.Errorf("MaxDepthLayers() project = %v, want 5", project)
	}
}

func TestDefault_DoesNotSetMaxDepth(t *testing.T) {
	if config.Default().Collaboration.MaxDepth != nil {
		t.Error("Default() must not set collaboration.max_depth — the numeric default lives in assistants.ResolveDepth")
	}
}
