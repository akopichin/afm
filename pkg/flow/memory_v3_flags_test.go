package flow_test

import (
	"os"
	"path/filepath"
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
