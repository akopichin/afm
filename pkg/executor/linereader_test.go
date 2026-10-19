package executor

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestLineReaderHandlesLargeAndUnterminatedLines(t *testing.T) {
	large := strings.Repeat("x", 2<<20)
	var got []string
	err := lineReader(strings.NewReader(large+"\r\nlast"), func(line string) bool {
		got = append(got, line)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{large, "last"}) {
		t.Fatal("large or unterminated line was lost")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestLineReaderReturnsReadError(t *testing.T) {
	if err := lineReader(failingReader{}, func(string) bool { return true }); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want %v", err, io.ErrUnexpectedEOF)
	}
}

// endlessReader yields n bytes of 'x' without a newline: a line that never ends.
type endlessReader struct{ n int }

func (r *endlessReader) Read(b []byte) (int, error) {
	if r.n == 0 {
		return 0, io.EOF
	}
	k := min(len(b), r.n)
	for i := range b[:k] {
		b[i] = 'x'
	}
	r.n -= k
	return k, nil
}

// A line longer than maxOutputLineBytes is cut there and marked, its tail is skipped up to the next newline,
// and the lines after it arrive whole: one runaway line cannot grow memory without bound.
func TestLineReaderTruncatesOverlongLines(t *testing.T) {
	var got []string
	r := io.MultiReader(&endlessReader{n: 3 * maxOutputLineBytes}, strings.NewReader("\r\nnext\n\nlast"))
	if err := lineReader(r, func(line string) bool {
		got = append(got, line)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{strings.Repeat("x", maxOutputLineBytes) + truncationMarker, "next", "", "last"}
	if !reflect.DeepEqual(got, want) {
		lens := make([]int, len(got))
		for i, l := range got {
			lens[i] = len(l)
		}
		t.Fatalf("line lengths = %v, want the first cut at %d bytes and marked, then next, an empty line, last", lens, maxOutputLineBytes)
	}
}
