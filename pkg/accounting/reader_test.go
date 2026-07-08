package accounting_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/proxy"
)

// writeFile пишет content в <dir>/<name>, создавая dir при необходимости.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestReaderSignature — контрактный тест: ResultUsage имеет 8 объявленных полей,
// LoadUsageRecords и ReadResultUsage компилируются с объявленными сигнатурами.
// Ожидается провал до создания reader.go.
func TestReaderSignature(t *testing.T) {
	ru := accounting.ResultUsage{
		StageID:             "design",
		InputTokens:         1,
		OutputTokens:        2,
		CacheCreationTokens: 3,
		CacheReadTokens:     4,
		TotalCostUsd:        0.5,
		DurationMs:          100,
		SessionID:           "sess",
	}
	if ru.StageID == "" {
		t.Fatalf("ResultUsage fields not populated: %+v", ru)
	}

	records, err := accounting.LoadUsageRecords("does-not-exist.jsonl")
	_ = records
	_ = err

	usage, ok := accounting.ReadResultUsage("does-not-exist.jsonl")
	_ = usage
	_ = ok
}

// TestLoadUsageRecordsReadsExistingRecords — две корректные строки usage.jsonl
// → две записи, первая с InputTokens==1200.
func TestLoadUsageRecordsReadsExistingRecords(t *testing.T) {
	dir := t.TempDir()
	content := `{"timestamp":"2026-07-07T10:00:00Z","model":"claude-sonnet-5","inputTokens":1200,"outputTokens":340,"cacheCreationTokens":0,"cacheReadTokens":800,"requestBytes":2048,"responseBytes":6144}
{"timestamp":"2026-07-07T10:01:00Z","model":"claude-sonnet-5","inputTokens":500,"outputTokens":100,"cacheCreationTokens":0,"cacheReadTokens":0,"requestBytes":1024,"responseBytes":3072}
`
	path := writeFile(t, dir, "usage.jsonl", content)

	records, err := accounting.LoadUsageRecords(path)
	if err != nil {
		t.Fatalf("LoadUsageRecords: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}
	if records[0].InputTokens != 1200 {
		t.Errorf("records[0].InputTokens = %d, want 1200", records[0].InputTokens)
	}
	if records[0].Model != "claude-sonnet-5" {
		t.Errorf("records[0].Model = %q, want claude-sonnet-5", records[0].Model)
	}
}

// TestLoadUsageRecordsMissingFileReturnsEmptyNotError — путь не существует →
// пустой срез (не nil-паника), nil error.
func TestLoadUsageRecordsMissingFileReturnsEmptyNotError(t *testing.T) {
	records, err := accounting.LoadUsageRecords(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("LoadUsageRecords missing file: unexpected err %v", err)
	}
	if len(records) != 0 {
		t.Errorf("len(records) = %d, want 0 for missing file", len(records))
	}
}

// TestLoadUsageRecordsSkipsTruncatedTrailingLine — две корректные строки + одна
// обрезанная третья → две записи сохранены по порядку, nil error.
func TestLoadUsageRecordsSkipsTruncatedTrailingLine(t *testing.T) {
	dir := t.TempDir()
	content := `{"timestamp":"2026-07-07T10:00:00Z","model":"claude-sonnet-5","inputTokens":1200,"outputTokens":340,"cacheCreationTokens":0,"cacheReadTokens":800,"requestBytes":2048,"responseBytes":6144}
{"timestamp":"2026-07-07T10:01:00Z","model":"claude-sonnet-5","inputTokens":500,"outputTokens":100,"cacheCreationTokens":0,"cacheReadTokens":0,"requestBytes":1024,"responseBytes":3072}
{"timestamp":"2026-07-07T10:02:00Z","model":"claude-sonnet-5","inputTokens":9
`
	path := writeFile(t, dir, "usage.jsonl", content)

	records, err := accounting.LoadUsageRecords(path)
	if err != nil {
		t.Fatalf("LoadUsageRecords: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2 (truncated trailing line skipped)", len(records))
	}
	if records[0].InputTokens != 1200 || records[1].InputTokens != 500 {
		t.Errorf("records = [%d, %d], want [1200, 500]", records[0].InputTokens, records[1].InputTokens)
	}
}

// TestReadResultUsageParsesTerminalResultEvent — implementation.jsonl со строкой
// assistant, затем реальной строкой result → ok=true и все поля ResultUsage
// точно совпадают с источником (StageID пуст — его проставляет вызывающая
// сторона).
func TestReadResultUsageParsesTerminalResultEvent(t *testing.T) {
	dir := t.TempDir()
	content := `{"type":"assistant","message":{"id":"msg_01"}}
{"type":"result","total_cost_usd":0.42,"usage":{"input_tokens":5000,"output_tokens":600,"cache_creation_input_tokens":100,"cache_read_input_tokens":200},"session_id":"sess-123","duration_ms":210681}
`
	path := writeFile(t, dir, "implementation.jsonl", content)

	usage, ok := accounting.ReadResultUsage(path)
	if !ok {
		t.Fatal("ok = false, want true (result event present)")
	}
	if usage.InputTokens != 5000 {
		t.Errorf("InputTokens = %d, want 5000", usage.InputTokens)
	}
	if usage.OutputTokens != 600 {
		t.Errorf("OutputTokens = %d, want 600", usage.OutputTokens)
	}
	if usage.CacheCreationTokens != 100 {
		t.Errorf("CacheCreationTokens = %d, want 100", usage.CacheCreationTokens)
	}
	if usage.CacheReadTokens != 200 {
		t.Errorf("CacheReadTokens = %d, want 200", usage.CacheReadTokens)
	}
	if usage.TotalCostUsd != 0.42 {
		t.Errorf("TotalCostUsd = %v, want 0.42", usage.TotalCostUsd)
	}
	if usage.DurationMs != 210681 {
		t.Errorf("DurationMs = %d, want 210681", usage.DurationMs)
	}
	if usage.SessionID != "sess-123" {
		t.Errorf("SessionID = %q, want sess-123", usage.SessionID)
	}
	if usage.StageID != "" {
		t.Errorf("StageID = %q, want empty (caller-supplied, not parsed)", usage.StageID)
	}
}

// TestReadResultUsageMissingFileReturnsNotOk — файл не существует → ok=false,
// без паники/ошибки.
func TestReadResultUsageMissingFileReturnsNotOk(t *testing.T) {
	usage, ok := accounting.ReadResultUsage(filepath.Join(t.TempDir(), "absent.jsonl"))
	if ok {
		t.Fatal("ok = true, want false for missing file")
	}
	if usage != (accounting.ResultUsage{}) {
		t.Errorf("usage = %+v, want zero ResultUsage for missing file", usage)
	}
}

// TestReadResultUsageAllZeroUsageStillOk — реальное наблюдаемое событие
// is_error:true с обнулённой usage разборчиво и даёт ok=true (ok — про
// разборчивость, не про значения полей).
func TestReadResultUsageAllZeroUsageStillOk(t *testing.T) {
	dir := t.TempDir()
	content := `{"type":"result","total_cost_usd":0.006087,"usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"session_id":"sess-zero","duration_ms":210681}
`
	path := writeFile(t, dir, "review.jsonl", content)

	usage, ok := accounting.ReadResultUsage(path)
	if !ok {
		t.Fatal("ok = false, want true (zero-usage result is still parseable)")
	}
	if usage.InputTokens != 0 || usage.OutputTokens != 0 {
		t.Errorf("usage tokens = (%d, %d), want (0, 0)", usage.InputTokens, usage.OutputTokens)
	}
}

// компиляционная гарантия: proxy.UsageRecord используется в сигнатуре.
var _ = proxy.UsageRecord{}
