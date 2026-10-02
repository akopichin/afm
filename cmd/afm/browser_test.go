package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/browser"
)

// TestCodexBrowserAdapterScript runs the shell-level test for the codex adapter's
// afm_chrome -c overrides so it is covered by `go test ./...` (CI), not only by a
// manual script invocation. Skips when bash/jq are unavailable.
func TestCodexBrowserAdapterScript(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "test-codex-browser.sh"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", script).CombinedOutput()
	if err != nil {
		t.Fatalf("codex browser adapter script failed: %v\n%s", err, out)
	}
}

func TestBrowserStatus_JSON_NoSecret(t *testing.T) {
	dir := t.TempDir()
	descPath := filepath.Join(dir, "desc.json")
	if err := browser.WriteInvocationDescriptor(descPath, browser.InvocationDescriptor{
		WSEndpoint: "ws://host.docker.internal:1/session/ep/build",
		Token:      "super-secret-xyz",
		AppURL:     "http://127.0.0.1:18080",
		Roots:      []string{dir},
	}); err != nil {
		t.Fatal(err)
	}
	out, err := browserStatusJSON(descPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "super-secret-xyz") {
		t.Fatal("status must never leak the transport token")
	}
	var s struct {
		Available bool   `json:"available"`
		AppURL    string `json:"app_url"`
	}
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("status must be valid JSON: %v", err)
	}
	if !s.Available || s.AppURL != "http://127.0.0.1:18080" {
		t.Fatalf("status wrong: %s", out)
	}
}

func TestBrowserStatus_NoDescriptor_NotAvailable(t *testing.T) {
	out, err := browserStatusJSON("")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Available bool `json:"available"`
	}
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatal(err)
	}
	if s.Available {
		t.Fatal("no descriptor must report unavailable")
	}
}

func TestBrowserProtocolVersionString(t *testing.T) {
	if got := browserProtocolVersionString(); got != "1" {
		t.Fatalf("want protocol version 1, got %q", got)
	}
}
