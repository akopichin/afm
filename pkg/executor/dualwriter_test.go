package executor

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// erroringWriter always fails — models a full disk (ENOSPC) or otherwise
// broken .stderr.log file descriptor.
type erroringWriter struct{}

func (erroringWriter) Write(p []byte) (int, error) {
	return 0, errors.New("simulated write failure (e.g. ENOSPC)")
}

func TestRunScript_StderrStreamsEvenWhenLogWriteFails(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")

	var mu sync.Mutex
	var stderrLines []string
	ex := New(Config{
		Command:     "bash",
		ExtraArgs:   []string{"-c", "echo boom 1>&2"},
		IdleTimeout: time.Minute,
		OnAction: func(stream, line string) {
			if stream != "stderr" {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			stderrLines = append(stderrLines, line)
		},
	})
	ex.stderrSinkOverride = func(string) (io.Writer, func()) {
		return erroringWriter{}, func() {}
	}

	if err := ex.RunScript(context.Background(), 5*time.Second, logFile); err != nil {
		t.Fatalf("RunScript: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(stderrLines) == 0 || stderrLines[0] != "boom" {
		t.Fatalf("expected stderr line to reach OnAction despite a failing file sink, got %v", stderrLines)
	}
}
