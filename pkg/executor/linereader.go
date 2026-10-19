package executor

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

// maxOutputLineBytes caps one line lineReader keeps in memory. It matches the 16 MiB line limit the dashboard
// server uses when it reads the same stage logs back (pkg/server), so a line afm keeps whole stays readable.
const maxOutputLineBytes = 16 << 20

// lineReader reads lines from r, calling fn for each line without its "\n" or "\r\n".
// A line longer than maxOutputLineBytes is cut there and ends with truncationMarker; the rest of it, up to the
// next newline, is skipped. Returns when r is exhausted or fn returns false.
func lineReader(r io.Reader, fn func(line string) bool) error {
	reader := bufio.NewReader(r)
	var line []byte
	cut := false
	for {
		chunk, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			line, cut = appendUpTo(line, cut, chunk)
			continue
		}
		line, cut = appendUpTo(line, cut, bytes.TrimSuffix(chunk, []byte("\n")))
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

// appendUpTo appends b to line while line stays within maxOutputLineBytes and reports whether line was cut.
func appendUpTo(line []byte, cut bool, b []byte) ([]byte, bool) {
	if cut {
		return line, true
	}
	if room := maxOutputLineBytes - len(line); len(b) > room {
		return append(line, b[:room]...), true
	}
	return append(line, b...), false
}
