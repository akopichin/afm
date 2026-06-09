package progress_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/progress"
)

func TestLoggerWritesTimestamped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	lg, err := progress.NewLogger(path)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer lg.Close()

	lg.Log("hello world")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "hello world") {
		t.Errorf("log content missing message: %q", content)
	}
	if !strings.Contains(content, "-") {
		t.Errorf("log content missing timestamp: %q", content)
	}
}

func TestLoggerRestartsWithSeparator(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	lg, err := progress.NewLogger(path)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	lg.Log("first run")
	lg.Close()

	lg2, err := progress.NewLogger(path)
	if err != nil {
		t.Fatalf("second NewLogger: %v", err)
	}
	defer lg2.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "restarted") {
		t.Errorf("should have restart separator: %q", content)
	}
	if !strings.Contains(content, "first run") {
		t.Errorf("original content should be preserved: %q", content)
	}
}

func TestFileLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	lock1, err := progress.NewLock(lockPath)
	if err != nil {
		t.Fatalf("NewLock: %v", err)
	}
	if err := lock1.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}

	lock2, _ := progress.NewLock(lockPath)
	if lock2.TryLock() == nil {
		t.Error("second TryLock should fail while first holds lock")
	}

	lock1.Unlock()

	if err := lock2.TryLock(); err != nil {
		t.Errorf("TryLock after release: %v", err)
	}
	lock2.Unlock()
}

func TestLoggerStartEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	lg, err := progress.NewLogger(path)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	lg.LogStart("implementation", "auth-module")
	lg.LogAction("Write", "pkg/auth/jwt.go")
	lg.LogAction("Bash", "git commit -m \"feat: jwt\"")
	lg.LogEnd(nil)
	lg.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "implementation agent") {
		t.Errorf("missing agent type: %q", content)
	}
	if !strings.Contains(content, "auth-module") {
		t.Errorf("missing stage name: %q", content)
	}
	if !strings.Contains(content, "started:") {
		t.Errorf("missing start timestamp: %q", content)
	}
	if !strings.Contains(content, "Write") {
		t.Errorf("missing tool action: %q", content)
	}
	if !strings.Contains(content, "pkg/auth/jwt.go") {
		t.Errorf("missing action detail: %q", content)
	}
	if !strings.Contains(content, "completed") {
		t.Errorf("missing completion marker: %q", content)
	}
	if !strings.Contains(content, "duration:") {
		t.Errorf("missing duration: %q", content)
	}
}

func TestLoggerEndWithError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	lg, err := progress.NewLogger(path)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	lg.LogStart("review", "auth-module")
	lg.LogEnd(errors.New("idle timeout after 30m"))
	lg.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "FAILED") {
		t.Errorf("missing FAILED marker: %q", content)
	}
	if !strings.Contains(content, "idle timeout after 30m") {
		t.Errorf("missing error message: %q", content)
	}
}
