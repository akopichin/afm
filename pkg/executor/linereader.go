package executor

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"math"
	"strings"
)

// maxOutputLineBytes caps one line of a live agent's stdout that afm keeps in memory. It matches the 16 MiB line
// limit the dashboard server uses when it reads the same stage logs back (pkg/server), so a line afm keeps
// whole stays readable.
const maxOutputLineBytes = 16 << 20

// lineReader reads lines from r, calling fn for each whole line without its "\n" or "\r\n", however long it is.
// Use it for logs afm already stored: replaying them must see exactly the lines the live run saw.
// Returns when r is exhausted or fn returns false.
func lineReader(r io.Reader, fn func(line string) bool) error {
	return readLines(r, math.MaxInt, fn)
}

// agentOutputLineReader is lineReader for a live agent's output, which afm does not control: a line longer
// than maxOutputLineBytes is cut there and ends with truncationMarker, and the rest of it, up to the next
// newline, is skipped.
func agentOutputLineReader(r io.Reader, fn func(line string) bool) error {
	return readLines(r, maxOutputLineBytes, fn)
}

// readLines reads lines from r into fn, cutting a line at limit bytes; see agentOutputLineReader.
func readLines(r io.Reader, limit int, fn func(line string) bool) error {
	reader := bufio.NewReader(r)
	var line []byte
	cut := false
	for {
		chunk, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			line, cut = appendUpTo(line, cut, chunk, limit)
			continue
		}
		line, cut = appendUpTo(line, cut, bytes.TrimSuffix(chunk, []byte("\n")), limit)
		if err == nil || len(line) > 0 || cut {
			text := string(line)
			if cut {
				text += truncationMarker
			} else {
				text = strings.TrimSuffix(text, "\r")
			}
			if !fn(text) {
				return nil
			}
		}
		line, cut = line[:0], false
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// appendUpTo appends b to line while line stays within limit bytes and reports whether line was cut.
func appendUpTo(line []byte, cut bool, b []byte, limit int) ([]byte, bool) {
	if cut {
		return line, true
	}
	if room := limit - len(line); len(b) > room {
		return append(line, b[:room]...), true
	}
	return append(line, b...), false
}
