package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLineWriter_EmitsPerLine(t *testing.T) {
	var got []string
	lw := newLineWriter(func(s string) { got = append(got, s) })
	lw.Write([]byte("a\nb\n"))
	lw.Write([]byte("c")) // partial, no newline yet
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("per-line: got %v", got)
	}
	lw.Flush()
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("flush trailing partial: got %v", got)
	}
	lw.Flush() // idempotent: no duplicate "c"
	if len(got) != 3 {
		t.Fatalf("double flush emitted extra: %v", got)
	}
}

func TestLineWriter_SplitAcrossWrites(t *testing.T) {
	var got []string
	lw := newLineWriter(func(s string) { got = append(got, s) })
	lw.Write([]byte("hel"))
	lw.Write([]byte("lo\nworld\n"))
	if !reflect.DeepEqual(got, []string{"hello", "world"}) {
		t.Fatalf("split-across-writes: got %v", got)
	}
}

func TestReadStderrTail_LastLinesCapped(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")
	stderr := filepath.Join(dir, "script.stderr.log")
	var lines []string
	for i := 1; i <= 50; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	if err := os.WriteFile(stderr, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tail := ReadStderrTail(logFile, 20, 4096)
	if strings.Contains(tail, "line 30") || !strings.Contains(tail, "line 50") || !strings.Contains(tail, "line 31") {
		t.Fatalf("expected last 20 lines (31..50), got:\n%s", tail)
	}
}

func TestReadStderrTail_Missing_ReturnsEmpty(t *testing.T) {
	if ReadStderrTail(filepath.Join(t.TempDir(), "nope.log"), 20, 4096) != "" {
		t.Fatal("missing file must return empty")
	}
}

func TestReadStderrTail_SingleHugeLine_CappedToBytes(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")
	if err := os.WriteFile(filepath.Join(dir, "script.stderr.log"), []byte(strings.Repeat("x", 10000)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tail := ReadStderrTail(logFile, 20, 4096)
	if len(tail) > 4096 {
		t.Fatalf("byte cap violated: %d bytes", len(tail))
	}
	if !utf8.ValidString(tail) {
		t.Fatal("cut on non-rune boundary")
	}
}

func TestReadStderrTail_LargeFile_MemoryBounded(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")
	// 5 MB of lines; must not load it all — just assert it returns the tail lines cheaply.
	var b strings.Builder
	for i := 0; i < 200000; i++ {
		fmt.Fprintf(&b, "noise %d\n", i)
	}
	b.WriteString("FINAL-LINE\n")
	if err := os.WriteFile(filepath.Join(dir, "script.stderr.log"), []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
	tail := ReadStderrTail(logFile, 20, 4096)
	if !strings.Contains(tail, "FINAL-LINE") {
		t.Fatalf("tail missing final line: %q", tail)
	}
}

func TestLineWriter_HugeLineNoNewline_TruncatesNotUnbounded(t *testing.T) {
	var got []string
	lw := newLineWriter(func(s string) { got = append(got, s) })
	lw.Write([]byte(strings.Repeat("a", 200000))) // no newline, > maxLineBytes
	lw.Write([]byte("\n"))
	if len(got) != 1 {
		t.Fatalf("expected one (truncated) line, got %d", len(got))
	}
	if len(got[0]) > 64*1024+32 {
		t.Fatalf("buffered line not capped: %d", len(got[0]))
	}
	if !strings.Contains(got[0], "[truncated]") {
		t.Fatalf("missing truncation marker: %.40q", got[0])
	}
}
