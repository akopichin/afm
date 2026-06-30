package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDialogTranscript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "planning.jsonl")
	lines := `{"type":"assistant","message":{"content":[{"type":"text","text":"Представляю дизайн."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__afm__ask_user","input":{"id":"q1","question":"Дизайн ок?"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__afm__ask_user","input":{"id":"q1","question":"Дизайн ок? Утверждаем?"}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"   "}]}}
{"type":"result","subtype":"success"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Пишу план."}]}}
`
	if err := os.WriteFile(path, []byte(lines), 0644); err != nil {
		t.Fatal(err)
	}

	items := DialogTranscript(path)
	want := []TranscriptItem{
		{Text: "Представляю дизайн."},
		{AskUserID: "q1"},
		{Text: "Пишу план."},
	}
	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d: %+v", len(items), len(want), items)
	}
	for i := range want {
		if items[i] != want[i] {
			t.Errorf("item %d: got %+v, want %+v", i, items[i], want[i])
		}
	}
}

func TestDialogTranscriptMissingFile(t *testing.T) {
	if items := DialogTranscript(filepath.Join(t.TempDir(), "nope.jsonl")); items != nil {
		t.Errorf("expected nil for missing file, got %+v", items)
	}
}
