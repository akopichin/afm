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
