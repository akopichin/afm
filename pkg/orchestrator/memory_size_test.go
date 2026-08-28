package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileExceeds(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.md")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", 100)), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !fileExceeds(p, 50) {
		t.Error("100 > 50 should exceed")
	}
	if fileExceeds(p, 200) {
		t.Error("100 < 200 should not exceed")
	}
	if fileExceeds(filepath.Join(dir, "missing.md"), 0) {
		t.Error("missing file must not exceed")
	}
}

func TestFifoDropOldestBlocks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.md")
	content := "# PROJECT MEMORY\n\n## Block A\n" + strings.Repeat("a", 200) +
		"\n## Block B\n" + strings.Repeat("b", 200) + "\n## Block C\n" + strings.Repeat("c", 50) + "\n"
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := fifoDropOldestBlocks(p, 300); err != nil {
		t.Fatalf("drop: %v", err)
	}
	out, _ := os.ReadFile(p)
	s := string(out)
	if !strings.HasPrefix(s, "# PROJECT MEMORY") {
		t.Error("header must be preserved")
	}
	if strings.Contains(s, "Block A") {
		t.Error("oldest block A should have been dropped")
	}
	if !strings.Contains(s, "Block C") {
		t.Error("newest block C must survive")
	}
	if len(out) > 300 {
		t.Errorf("size %d still over 300", len(out))
	}
}

func TestLineLimitForBytes(t *testing.T) {
	if lineLimitForBytes(20000) < 10 {
		t.Error("line limit too small")
	}
	if lineLimitForBytes(0) < 10 {
		t.Error("line limit must have a floor of 10")
	}
}
