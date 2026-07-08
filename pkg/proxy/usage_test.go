package proxy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/proxy"
)

// TestUsageContract_Signatures is a compile-time guard that the three new
// entities in pkg/proxy/usage.go exist with the declared signatures and that
// UsageRecord exposes all eight documented fields with the correct Go types.
func TestUsageContract_Signatures(t *testing.T) {
	var r proxy.UsageRecord

	// Field existence + declared Go types.
	var ts = r.Timestamp
	var model = r.Model
	var inputTokens = r.InputTokens
	var outputTokens = r.OutputTokens
	var cacheCreationTokens = r.CacheCreationTokens
	var cacheReadTokens = r.CacheReadTokens
	var requestBytes = r.RequestBytes
	var responseBytes = r.ResponseBytes
	_, _, _, _, _, _, _, _ = ts, model, inputTokens, outputTokens,
		cacheCreationTokens, cacheReadTokens, requestBytes, responseBytes

	// Routine signatures.
	var _ = proxy.ParseUsage
	var _ = proxy.AppendUsageRecord
}

func TestParseUsage_ParsesNonStreamingJsonResponse(t *testing.T) {
	contentType := "application/json"
	body := `{"model":"claude-sonnet-5","usage":{"input_tokens":1200,"output_tokens":340,"cache_creation_input_tokens":0,"cache_read_input_tokens":800}}`

	record, err := proxy.ParseUsage(contentType, body)
	if err != nil {
		t.Fatalf("ParseUsage: unexpected error: %v", err)
	}
	if record.Model != "claude-sonnet-5" {
		t.Errorf("Model: got %q, want claude-sonnet-5", record.Model)
	}
	if record.InputTokens != 1200 {
		t.Errorf("InputTokens: got %d, want 1200", record.InputTokens)
	}
	if record.OutputTokens != 340 {
		t.Errorf("OutputTokens: got %d, want 340", record.OutputTokens)
	}
	if record.CacheCreationTokens != 0 {
		t.Errorf("CacheCreationTokens: got %d, want 0", record.CacheCreationTokens)
	}
	if record.CacheReadTokens != 800 {
		t.Errorf("CacheReadTokens: got %d, want 800", record.CacheReadTokens)
	}
}

func TestParseUsage_ParsesSseStreamResponse(t *testing.T) {
	contentType := "text/event-stream"
	sseBody := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg1","model":"claude-sonnet-5","usage":{"input_tokens":1200,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":800}}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":340}}`,
		"",
	}, "\n")

	record, err := proxy.ParseUsage(contentType, sseBody)
	if err != nil {
		t.Fatalf("ParseUsage: unexpected error: %v", err)
	}
	if record.Model != "claude-sonnet-5" {
		t.Errorf("Model: got %q, want claude-sonnet-5", record.Model)
	}
	if record.InputTokens != 1200 {
		t.Errorf("InputTokens: got %d, want 1200", record.InputTokens)
	}
	if record.OutputTokens != 340 {
		t.Errorf("OutputTokens: got %d, want 340 (merged from message_delta)", record.OutputTokens)
	}
	if record.CacheReadTokens != 800 {
		t.Errorf("CacheReadTokens: got %d, want 800", record.CacheReadTokens)
	}
}

func TestParseUsage_ReturnsErrorOnNon200(t *testing.T) {
	contentType := "application/json"
	// A non-200 error body carries no usage field.
	body := `{"type":"error","error":{"type":"overloaded_error","message":"server overloaded"}}`

	record, err := proxy.ParseUsage(contentType, body)
	if err == nil {
		t.Fatal("ParseUsage: expected error for body with no usage field, got nil")
	}
	if record != (proxy.UsageRecord{}) {
		t.Errorf("ParseUsage: expected zero-valued record on error, got %+v", record)
	}
}

func TestAppendUsageRecord_WritesOneJsonLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")

	record := proxy.UsageRecord{
		Timestamp:           time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
		Model:               "claude-sonnet-5",
		InputTokens:         1200,
		OutputTokens:        340,
		CacheCreationTokens: 0,
		CacheReadTokens:     800,
		RequestBytes:        2048,
		ResponseBytes:       6144,
	}

	if err := proxy.AppendUsageRecord(path, record); err != nil {
		t.Fatalf("AppendUsageRecord: unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back usage log: %v", err)
	}

	trimmed := strings.TrimRight(string(data), "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one line, got %d: %q", len(lines), string(data))
	}

	var got proxy.UsageRecord
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal written line: %v (line: %s)", err, lines[0])
	}
	if !got.Timestamp.Equal(record.Timestamp) {
		t.Errorf("Timestamp: got %v, want %v", got.Timestamp, record.Timestamp)
	}
	if got.Model != record.Model {
		t.Errorf("Model: got %q, want %q", got.Model, record.Model)
	}
	if got.InputTokens != record.InputTokens {
		t.Errorf("InputTokens: got %d, want %d", got.InputTokens, record.InputTokens)
	}
	if got.OutputTokens != record.OutputTokens {
		t.Errorf("OutputTokens: got %d, want %d", got.OutputTokens, record.OutputTokens)
	}
	if got.CacheCreationTokens != record.CacheCreationTokens {
		t.Errorf("CacheCreationTokens: got %d, want %d", got.CacheCreationTokens, record.CacheCreationTokens)
	}
	if got.CacheReadTokens != record.CacheReadTokens {
		t.Errorf("CacheReadTokens: got %d, want %d", got.CacheReadTokens, record.CacheReadTokens)
	}
	if got.RequestBytes != record.RequestBytes {
		t.Errorf("RequestBytes: got %d, want %d", got.RequestBytes, record.RequestBytes)
	}
	if got.ResponseBytes != record.ResponseBytes {
		t.Errorf("ResponseBytes: got %d, want %d", got.ResponseBytes, record.ResponseBytes)
	}
}

func TestAppendUsageRecord_ReturnsErrorOnUnwritablePath(t *testing.T) {
	err := proxy.AppendUsageRecord("/nonexistent-dir/usage.jsonl", proxy.UsageRecord{})
	if err == nil {
		t.Fatal("AppendUsageRecord: expected error for unwritable path, got nil")
	}
}
