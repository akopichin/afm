package orchestrator

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishLifecycleHookFailure_Notice(t *testing.T) {
	o := newTestOrchestrator(t) // хелпер из Task 8
	o.PublishLifecycleHookFailure("telegram", "run-1:flow:flow_started", errors.New("exit status 1"))

	raw, err := os.ReadFile(filepath.Join(o.opts.RunDir, "notices.jsonl"))
	if err != nil {
		t.Fatalf("notices.jsonl: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw[:len(raw)-1], &got); err != nil { // последняя строка
		t.Fatalf("parse notice: %v (%s)", err, raw)
	}
	if got["type"] != "lifecycle_hook_failed" {
		t.Fatalf("type: %v", got["type"])
	}
	data, _ := got["data"].(map[string]any)
	if data["hook_id"] != "telegram" || data["error"] != "exit status 1" {
		t.Fatalf("data: %+v", data)
	}
}
