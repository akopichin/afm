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

func TestReadStderrTail_MultiByteRuneAtCut_NeverSplitsRune(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")

	// ASCII padding + Cyrillic text + an emoji (4-byte UTF-8), so that a small maxBytes cut is very likely to
	// fall inside one of the multi-byte characters rather than exactly on a boundary.
	line := strings.Repeat("x", 10) + "привет мир 🎉 конец"
	if err := os.WriteFile(filepath.Join(dir, "script.stderr.log"), []byte(line+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	for maxBytes := 1; maxBytes <= len(line); maxBytes++ {
		tail := ReadStderrTail(logFile, 20, maxBytes)
		if !utf8.ValidString(tail) {
			t.Fatalf("maxBytes=%d: cut split a multi-byte rune: %q", maxBytes, tail)
		}
	}
}

func TestReadStderrTail_HugeNewlineFreeLine_NonEmptyCapped(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")
	// 200KB single character, no trailing newline — file size (200KB) exceeds the seek window (maxBytes*4 +
	// 64KiB) for a small maxBytes, and the window itself contains no '\n' anywhere.
	if err := os.WriteFile(filepath.Join(dir, "script.stderr.log"), []byte(strings.Repeat("x", 200*1024)), 0644); err != nil {
		t.Fatal(err)
	}

	const maxBytes = 4096
	tail := ReadStderrTail(logFile, 20, maxBytes)
	if tail == "" {
		t.Fatal("expected a non-empty tail for a huge newline-free line, got empty string")
	}
	if len(tail) > maxBytes {
		t.Fatalf("byte cap violated: %d bytes", len(tail))
	}
	if !utf8.ValidString(tail) {
		t.Fatal("tail is not valid UTF-8")
	}
}

func TestReadStderrTailFrom_SkipsPriorContent(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")
	stderr := filepath.Join(dir, "script.stderr.log")

	if err := os.WriteFile(stderr, []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	info, statErr := os.Stat(stderr)
	if statErr != nil {
		t.Fatal(statErr)
	}
	from := info.Size()

	f, err := os.OpenFile(stderr, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("new\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	tail := ReadStderrTailFrom(logFile, from, 20, 4096)
	if !strings.Contains(tail, "new") {
		t.Fatalf("tail missing content written after from: %q", tail)
	}
	if strings.Contains(tail, "old") {
		t.Fatalf("tail leaked content written before from: %q", tail)
	}

	// Nothing appended since from (which now equals the file size) → empty.
	info, statErr = os.Stat(stderr)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := ReadStderrTailFrom(logFile, info.Size(), 20, 4096); got != "" {
		t.Fatalf("from >= size must return empty, got %q", got)
	}
}

func TestReadStderrTailFrom_KeepsFirstLineAtOffset(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")
	stderr := filepath.Join(dir, "script.stderr.log")

	// Pad past the seek window (maxBytes*4 + 64KiB for maxBytes=4096 ⇒ 80KiB) so that, were the seek
	// position derived from the window instead of from `from`, this test would still land near the end —
	// but `from` itself sits right at the padding boundary, which is what must determine start here.
	padding := strings.Repeat("x", 80*1024) + "\n"
	if err := os.WriteFile(stderr, []byte(padding), 0644); err != nil {
		t.Fatal(err)
	}
	info, statErr := os.Stat(stderr)
	if statErr != nil {
		t.Fatal(statErr)
	}
	from := info.Size()

	f, err := os.OpenFile(stderr, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("ONE-LINE-AFTER-OFFSET\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	tail := ReadStderrTailFrom(logFile, from, 20, 4096)
	if tail != "ONE-LINE-AFTER-OFFSET" {
		t.Fatalf("expected the single line after offset to come back whole, got %q", tail)
	}
}

func TestReadStderrTailFrom_ZeroMatchesReadStderrTail(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")
	stderr := filepath.Join(dir, "script.stderr.log")

	var b strings.Builder
	for i := 0; i < 200000; i++ {
		fmt.Fprintf(&b, "noise %d\n", i)
	}
	b.WriteString("FINAL-LINE\n")
	if err := os.WriteFile(stderr, []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}

	got := ReadStderrTailFrom(logFile, 0, 20, 4096)
	want := ReadStderrTail(logFile, 20, 4096)
	if got != want {
		t.Fatalf("ReadStderrTailFrom(...,0,...) != ReadStderrTail(...): got %q, want %q", got, want)
	}
	if !strings.Contains(got, "FINAL-LINE") {
		t.Fatalf("missing final line: %q", got)
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
