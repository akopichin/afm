package flow_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

func TestMemoryConfig_ProjectModeHelpers(t *testing.T) {
	cases := []struct {
		mode     string
		canRead  bool
		canWrite bool
	}{
		{"", false, false},
		{"r", true, false},
		{"w", false, true},
		{"rw", true, true},
	}
	for _, c := range cases {
		m := flow.MemoryConfig{Mode: c.mode}
		if m.CanReadProject() != c.canRead {
			t.Errorf("mode %q: CanReadProject = %v, want %v", c.mode, m.CanReadProject(), c.canRead)
		}
		if m.CanWriteProject() != c.canWrite {
			t.Errorf("mode %q: CanWriteProject = %v, want %v", c.mode, m.CanWriteProject(), c.canWrite)
		}
	}
}

func TestMemoryConfig_UseFor(t *testing.T) {
	tr, fa := true, false
	g := flow.MemoryConfig{MemoryUse: true}
	if !g.UseFor(nil) {
		t.Error("nil override must inherit global true")
	}
	if g.UseFor(&fa) {
		t.Error("stage &false must override global true")
	}
	d := flow.MemoryConfig{}
	if d.UseFor(nil) {
		t.Error("nil override must inherit global false")
	}
	if !d.UseFor(&tr) {
		t.Error("stage &true must override global false")
	}
}

func TestParseMemoryV3_ModeAndUseDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(p, []byte(`
name: f
memory:
  path: docs/memory
stages:
  - id: build
    name: build
    agents: [planning, implementation]
`), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := flow.ParseFile(p)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Memory.Mode != "rw" {
		t.Errorf("memory.mode default = %q, want rw", f.Memory.Mode)
	}
	if f.Memory.MemoryUse {
		t.Error("memory_use default must be false")
	}
}

func TestParseMemoryV3_StageMemoryUseOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(p, []byte(`
name: f
memory:
  path: docs/memory
  memory_use: true
stages:
  - id: a
    name: a
    agents: [planning]
    memory_use: false
  - id: b
    name: b
    agents: [planning]
`), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := flow.ParseFile(p)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !f.Memory.MemoryUse {
		t.Error("global memory_use should be true")
	}
	if f.Stages[0].MemoryUse == nil || *f.Stages[0].MemoryUse {
		t.Errorf("stage a memory_use should be explicit false, got %v", f.Stages[0].MemoryUse)
	}
	if f.Stages[1].MemoryUse != nil {
		t.Error("stage b memory_use should be nil (inherit)")
	}
}

func TestValidateMemoryV3_BadGlobalMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(p, []byte(`
name: f
memory:
  path: docs/memory
  mode: x
stages:
  - id: a
    name: a
    agents: [planning]
`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := flow.ParseFile(p); err == nil {
		t.Fatal("invalid memory.mode must be a parse error")
	}
}

func TestValidateMemoryV3_NegativeMaxRules(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(p, []byte(`
name: f
memory:
  path: docs/memory
  max_rules: -5
stages:
  - id: a
    name: a
    agents: [planning]
`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := flow.ParseFile(p); err == nil {
		t.Fatal("negative memory.max_rules must be a parse error")
	}
}

func TestValidateMemoryV3_ZeroMaxRulesDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(p, []byte(`
name: f
memory:
  path: docs/memory
  max_rules: 0
stages:
  - id: a
    name: a
    agents: [planning]
`), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := flow.ParseFile(p)
	if err != nil {
		t.Fatalf("max_rules: 0 must be treated as unset, not an error: %v", err)
	}
	if f.Memory.MaxRules != 25 {
		t.Errorf("max_rules: 0 should default to 25, got %d", f.Memory.MaxRules)
	}
}

// parseCapturingStderr parses body via ParseFile and returns whatever ParseFile
// wrote to os.Stderr (WARN lines), mirroring the os.Pipe pattern used for the
// deprecated-supervisor-keys warning test.
func parseCapturingStderr(t *testing.T, body string) (*flow.Flow, string, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	f, perr := flow.ParseFile(p)
	os.Stderr = orig
	w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return f, buf.String(), perr
}

func TestParseMemoryV3_SettingsWithoutPathWarn(t *testing.T) {
	f, stderr, err := parseCapturingStderr(t, `
name: f
memory:
  mode: r
  memory_use: true
  max_rules: 10
  commit: true
stages:
  - id: a
    name: a
    agents: [planning]
`)
	if err != nil {
		t.Fatalf("memory.* without path must stay non-fatal, got: %v", err)
	}
	if f.MemoryEnabled() {
		t.Error("memory must be disabled without a path")
	}
	if !strings.Contains(stderr, "WARN") || !strings.Contains(stderr, "memory.path") {
		t.Errorf("expected a WARN about memory.path, got stderr: %q", stderr)
	}
	for _, key := range []string{"mode", "memory_use", "max_rules", "commit"} {
		if !strings.Contains(stderr, key) {
			t.Errorf("expected WARN to name %q, got stderr: %q", key, stderr)
		}
	}
}

func TestParseMemoryV3_NoSpuriousWarnWhenPathSet(t *testing.T) {
	_, stderr, err := parseCapturingStderr(t, `
name: f
memory:
  path: docs/memory
  mode: r
stages:
  - id: a
    name: a
    agents: [planning]
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strings.Contains(stderr, "WARN") {
		t.Errorf("no WARN expected when memory.path is set, got: %q", stderr)
	}
}

func TestParseMemoryV3_NoWarnWhenMemoryBlockAbsent(t *testing.T) {
	_, stderr, err := parseCapturingStderr(t, `
name: f
stages:
  - id: a
    name: a
    agents: [planning]
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strings.Contains(stderr, "WARN") {
		t.Errorf("no WARN expected when there is no memory block, got: %q", stderr)
	}
}
