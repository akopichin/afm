package stagefiles

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAppendNotice(t *testing.T) {
	runDir := t.TempDir()

	AppendNotice(runDir, "stage-a", "agent_completed", "planning")
	AppendNotice(runDir, "stage-b", "context_warning", "dep-x: context too large")

	f, err := os.Open(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		t.Fatalf("notices.jsonl not created: %v", err)
	}
	defer f.Close()

	var lines []noticeEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e noticeEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("unmarshal line %q: %v", sc.Text(), err)
		}
		lines = append(lines, e)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[0].StageID != "stage-a" || lines[0].Type != "agent_completed" {
		t.Errorf("line 0 = %+v, want stage-a/agent_completed", lines[0])
	}
	if lines[1].StageID != "stage-b" || lines[1].Type != "context_warning" {
		t.Errorf("line 1 = %+v, want stage-b/context_warning", lines[1])
	}
	if lines[0].Time.IsZero() {
		t.Error("line 0 Time is zero, want a real timestamp")
	}
}

// TestAppendNotice_ConcurrentWritesNoCorruption reproduces the real race
// script stages now create: independent stdout- and stderr-reader
// goroutines (executor.RunScript) both call AppendNotice for the same run
// concurrently. Every appended line must remain valid, complete JSON — no
// interleaved/torn writes — and the total line count must match the number
// of concurrent callers.
func TestAppendNotice_ConcurrentWritesNoCorruption(t *testing.T) {
	runDir := t.TempDir()

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			AppendNotice(runDir, "stage-a", "script_output", fmt.Sprintf("line-%d", i))
		}(i)
	}
	wg.Wait()

	f, err := os.Open(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		t.Fatalf("notices.jsonl not created: %v", err)
	}
	defer f.Close()

	count := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e noticeEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("line %d is not valid JSON: %v (line: %q)", count, err, sc.Text())
		}
		count++
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanning notices.jsonl: %v", err)
	}
	if count != n {
		t.Errorf("got %d notice lines, want %d", count, n)
	}
}
