package serve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseConfigFull(t *testing.T) {
	dir := t.TempDir()
	opts := filepath.Join(dir, "options.yaml")
	if err := os.WriteFile(opts, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "projects:\n  - {id: app-1, path: " + dir + "}\n" +
		"pipelines:\n  - {id: dev, source: github.com/acme/pipe, options: " + opts + "}\n" +
		"limits: {max_running: 8, max_starting: 3}\n" +
		"retention: {release_after: 24h}\n"
	cfg, err := ParseConfig([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Projects[0]; got.ID != "app-1" || got.Path != dir {
		t.Errorf("project = %+v", got)
	}
	if got := cfg.Pipelines[0]; got.ID != "dev" || got.Options != opts {
		t.Errorf("pipeline = %+v", got)
	}
	if cfg.Limits.MaxRunning != 8 || cfg.Limits.MaxStarting != 3 {
		t.Errorf("limits = %+v", cfg.Limits)
	}
	if cfg.Retention.ReleaseAfter != 24*time.Hour {
		t.Errorf("release_after = %v", cfg.Retention.ReleaseAfter)
	}
}

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := ParseConfig([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Limits.MaxRunning != DefaultMaxRunning || cfg.Limits.MaxStarting != DefaultMaxStarting {
		t.Errorf("limits = %+v", cfg.Limits)
	}
	if cfg.Retention.ReleaseAfter != 0 {
		t.Errorf("retention must be off by default, got %v", cfg.Retention.ReleaseAfter)
	}
}

func TestParseConfigRejects(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, body, want string }{
		{"unknown top key", "extra: 1\n", "extra"},
		{"unknown project key", "projects: [{id: a, path: " + dir + ", x: 1}]\n", "x"},
		{"bad id", "projects: [{id: A_b, path: " + dir + "}]\n", "id"},
		{"empty id", "projects: [{path: " + dir + "}]\n", "id"},
		{"relative path", "projects: [{id: a, path: rel/dir}]\n", "absolute"},
		{"missing path", "projects: [{id: a, path: " + filepath.Join(dir, "nope") + "}]\n", "nope"},
		{"path is a file", "projects: [{id: a, path: " + file + "}]\n", "directory"},
		{"duplicate project", "projects: [{id: a, path: " + dir + "}, {id: a, path: " + dir + "}]\n", "duplicate"},
		{"pipeline without source", "pipelines: [{id: p}]\n", "source"},
		{"pipeline bad id", "pipelines: [{id: P, source: s}]\n", "id"},
		{"duplicate pipeline", "pipelines: [{id: p, source: s}, {id: p, source: s}]\n", "duplicate"},
		{"relative options", "pipelines: [{id: p, source: s, options: o.yaml}]\n", "absolute"},
		{"missing options", "pipelines: [{id: p, source: s, options: " + filepath.Join(dir, "nope") + "}]\n", "nope"},
		{"zero max_running", "limits: {max_running: 0}\n", "max_running"},
		{"negative max_starting", "limits: {max_starting: -1}\n", "max_starting"},
		{"negative retention", "retention: {release_after: -1h}\n", "release_after"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConfig([]byte(tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadConfigMissingFileIsEmpty(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "serve.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 0 || cfg.Limits.MaxRunning != DefaultMaxRunning {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestConfigLookup(t *testing.T) {
	dir := t.TempDir()
	cfg, err := ParseConfig([]byte("projects: [{id: a, path: " + dir + "}]\npipelines: [{id: p, source: s}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Project("a"); !ok {
		t.Error("project a not found")
	}
	if _, ok := cfg.Project("b"); ok {
		t.Error("project b must not be found")
	}
	if _, ok := cfg.Pipeline("p"); !ok {
		t.Error("pipeline p not found")
	}
}
