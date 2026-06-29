package proxy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/proxy"
)

func TestCreateShim(t *testing.T) {
	// Создаём поддельный claude в temp-dir и прописываем его в PATH.
	fakeClaudeDir := t.TempDir()
	fakeClaude := filepath.Join(fakeClaudeDir, "claude")
	if err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\necho fake"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeClaudeDir+":"+os.Getenv("PATH"))

	shimDir, err := proxy.CreateShim("http://127.0.0.1:9999")
	if err != nil {
		t.Fatalf("CreateShim: %v", err)
	}
	defer os.RemoveAll(shimDir) //nolint:errcheck // best-effort cleanup

	shimPath := filepath.Join(shimDir, "claude")
	info, err := os.Stat(shimPath)
	if err != nil {
		t.Fatalf("shim file missing: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Error("shim should be executable")
	}

	content, _ := os.ReadFile(shimPath)
	if !strings.Contains(string(content), "ANTHROPIC_BASE_URL=http://127.0.0.1:9999") {
		t.Errorf("shim should set proxy URL, got:\n%s", content)
	}
	if !strings.Contains(string(content), fakeClaude) {
		t.Errorf("shim should call real claude at %s, got:\n%s", fakeClaude, content)
	}
}

func TestCreateShim_NoClaude(t *testing.T) {
	// Claude не в PATH — ожидаем ошибку.
	t.Setenv("PATH", t.TempDir())

	_, err := proxy.CreateShim("http://127.0.0.1:9999")
	if err == nil {
		t.Error("expected error when claude not in PATH")
	}
}
