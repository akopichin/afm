package proxy_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/proxy"
)

func TestProxy_StartShutdown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	p := proxy.New(upstream.URL, nil, "")
	addr, err := p.Start(0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if addr == "" {
		t.Fatal("addr is empty after Start")
	}
	if p.Addr() != addr {
		t.Errorf("Addr() mismatch: %q vs %q", p.Addr(), addr)
	}

	resp, err := http.Get(addr + "/v1/messages")
	if err != nil {
		t.Fatalf("proxy GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body: got %s", body)
	}

	if err := p.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// TestProxyContract_NewThreeArgSignature is a compile-time guard that New accepts the
// three-argument signature declared in the contract: (upstream, transforms, usageLogPath).
func TestProxyContract_NewThreeArgSignature(t *testing.T) {
	var _ = proxy.New("https://upstream.example", nil, "")
}

// TestProxy_ServeHTTP_CapturesPassthroughUsage asserts that a request handled by
// passthrough (no matching Transform) against a Proxy with a non-empty usageLogPath
// appends exactly one UsageRecord after the response is sent. This is the
// "passthrough тоже считается" behavior the design calls out as the acceptance criterion.
func TestProxy_ServeHTTP_CapturesPassthroughUsage(t *testing.T) {
	upstreamBody := `{"model":"claude-sonnet-5","usage":{"input_tokens":100,"output_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":10}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(upstreamBody)) //nolint:errcheck
	}))
	defer upstream.Close()

	usageLog := filepath.Join(t.TempDir(), "usage.jsonl")
	p := proxy.New(upstream.URL, nil, usageLog) // no transforms → passthrough
	addr, err := p.Start(0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Shutdown(context.Background()) //nolint:errcheck

	resp, err := http.Post(addr+"/v1/messages", "application/json", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatalf("proxy POST: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Client-visible body must be exactly the upstream body.
	if string(body) != upstreamBody {
		t.Errorf("client body altered by tee-writer: got %q, want %q", body, upstreamBody)
	}

	data, err := os.ReadFile(usageLog)
	if err != nil {
		t.Fatalf("usage log should be written after a passthrough response: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one usage line, got %d: %q", len(lines), string(data))
	}

	var rec proxy.UsageRecord
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("unmarshal usage record: %v (line: %s)", err, lines[0])
	}
	if rec.Model != "claude-sonnet-5" {
		t.Errorf("Model: got %q, want claude-sonnet-5", rec.Model)
	}
	if rec.InputTokens != 100 {
		t.Errorf("InputTokens: got %d, want 100", rec.InputTokens)
	}
	if rec.OutputTokens != 20 {
		t.Errorf("OutputTokens: got %d, want 20", rec.OutputTokens)
	}
	if rec.ResponseBytes != len(upstreamBody) {
		t.Errorf("ResponseBytes: got %d, want %d", rec.ResponseBytes, len(upstreamBody))
	}
}

// TestProxy_ServeHTTP_DisabledCaptureDoesNotCreateFile asserts that a Proxy constructed
// with usageLogPath="" never creates/writes the target file even after a successful
// request (the no-op convention used in tests).
func TestProxy_ServeHTTP_DisabledCaptureDoesNotCreateFile(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"claude-sonnet-5","usage":{"input_tokens":1,"output_tokens":1}}`)) //nolint:errcheck
	}))
	defer upstream.Close()

	usageLog := filepath.Join(t.TempDir(), "usage.jsonl")
	p := proxy.New(upstream.URL, nil, "") // capture disabled
	addr, err := p.Start(0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Shutdown(context.Background()) //nolint:errcheck

	resp, err := http.Get(addr + "/v1/messages")
	if err != nil {
		t.Fatalf("proxy GET: %v", err)
	}
	io.ReadAll(resp.Body) //nolint:errcheck
	resp.Body.Close()

	if _, err := os.Stat(usageLog); !os.IsNotExist(err) {
		t.Errorf("usage log must not exist when capture is disabled; stat err: %v", err)
	}
}

// TestProxy_ServeHTTP_NoUsageCaptureOnNonOKStatus asserts that a non-200 passthrough
// response (errors, rate limits) is NOT captured as a usage record, even when the body
// carries a valid usage field and usageLogPath is set. The status-skip in captureUsage
// is what suppresses spurious warnings on error responses — a regression that removes
// the skip would write a record here because the body parses successfully.
func TestProxy_ServeHTTP_NoUsageCaptureOnNonOKStatus(t *testing.T) {
	upstreamBody := `{"model":"claude-sonnet-5","usage":{"input_tokens":100,"output_tokens":20}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests) // 429 — body carries usage, but status is an error
		w.Write([]byte(upstreamBody))             //nolint:errcheck
	}))
	defer upstream.Close()

	usageLog := filepath.Join(t.TempDir(), "usage.jsonl")
	p := proxy.New(upstream.URL, nil, usageLog) // no transforms → passthrough, capture enabled
	addr, err := p.Start(0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Shutdown(context.Background()) //nolint:errcheck

	resp, err := http.Post(addr+"/v1/messages", "application/json", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatalf("proxy POST: %v", err)
	}
	io.ReadAll(resp.Body) //nolint:errcheck
	resp.Body.Close()

	// Client must still see the upstream error status, forwarded transparently.
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status forwarded: got %d, want 429", resp.StatusCode)
	}

	// No usage record must be written for a non-200 response.
	if _, err := os.Stat(usageLog); !os.IsNotExist(err) {
		data, _ := os.ReadFile(usageLog)
		t.Errorf("usage log must not exist for non-200 response; stat err: %v, content: %q", err, string(data))
	}
}

// TestProxy_ServeHTTP_TeeDoesNotAlterClientResponse asserts the tee-writer forwards
// every client-visible byte unchanged and without blocking — the response body equals
// the upstream's body exactly, same as before this change.
func TestProxy_ServeHTTP_TeeDoesNotAlterClientResponse(t *testing.T) {
	upstreamBody := strings.Repeat("x", 4096)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(upstreamBody)) //nolint:errcheck
	}))
	defer upstream.Close()

	p := proxy.New(upstream.URL, nil, "")
	addr, err := p.Start(0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Shutdown(context.Background()) //nolint:errcheck

	resp, err := http.Get(addr + "/v1/messages")
	if err != nil {
		t.Fatalf("proxy GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if string(body) != upstreamBody {
		t.Errorf("tee-writer altered client body: got %d bytes, want %d", len(body), len(upstreamBody))
	}
}
