package executor

import (
	"bytes"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// maxLineBytes caps how much of a single line lineWriter will buffer in
// memory. A script that writes an unbounded line with no trailing newline
// (e.g. a runaway progress bar) must not grow the buffer forever: once the
// buffer would exceed maxLineBytes, the line is emitted early with a
// truncation marker, and the rest of the line (up to the next newline) is
// discarded.
const maxLineBytes = 64 * 1024

// truncationMarker is appended to a line that was cut off because it grew
// past maxLineBytes.
const truncationMarker = " …[truncated]"

// lineWriter is an io.Writer that splits incoming bytes on '\n' and calls
// emit once per complete line (the trailing '\n' is stripped). A trailing
// partial line (no terminating '\n' yet) is held in an internal buffer until
// either more bytes complete it or Flush is called.
//
// The buffer is bounded at maxLineBytes: once a still-unterminated line would
// exceed that size, lineWriter emits what it has so far with a
// truncationMarker suffix and discards any further bytes for that same line
// until the next '\n' is seen (so a single huge line never grows the buffer
// past maxLineBytes, and only ever produces one emitted line).
type lineWriter struct {
	emit    func(string)
	buf     []byte
	overLen bool // true while discarding the remainder of an over-long line
}

// newLineWriter returns a lineWriter that calls emit for each complete line
// written to it.
func newLineWriter(emit func(string)) *lineWriter {
	return &lineWriter{emit: emit}
}

// Write implements io.Writer.
func (lw *lineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		idx := bytes.IndexByte(p, '\n')
		if idx < 0 {
			lw.append(p)
			break
		}
		lw.append(p[:idx])
		lw.flushLine()
		p = p[idx+1:]
	}
	return n, nil
}

// append adds b to the current line buffer, truncating (and marking the line
// as over-long) if the result would exceed maxLineBytes.
func (lw *lineWriter) append(b []byte) {
	if lw.overLen {
		// Already discarding the tail of an over-long line; nothing more to
		// buffer until the next '\n'.
		return
	}
	remaining := maxLineBytes - len(lw.buf)
	if remaining <= 0 {
		lw.overLen = true
		return
	}
	if len(b) > remaining {
		lw.buf = append(lw.buf, b[:remaining]...)
		lw.overLen = true
		return
	}
	lw.buf = append(lw.buf, b...)
}

// flushLine emits the current buffered line (adding the truncation marker if
// it was cut short) and resets the buffer for the next line.
func (lw *lineWriter) flushLine() {
	line := string(lw.buf)
	if lw.overLen {
		line += truncationMarker
	}
	lw.emit(line)
	lw.buf = lw.buf[:0]
	lw.overLen = false
}

// Flush emits any buffered trailing partial line (a line with no terminating
// '\n' yet). It is idempotent: calling it again after the buffer has already
// been emitted (or when nothing was ever buffered) does nothing.
func (lw *lineWriter) Flush() {
	if len(lw.buf) == 0 && !lw.overLen {
		return
	}
	lw.flushLine()
}

// ReadStderrTail returns the last up-to-maxLines lines (capped at
// up-to-maxBytes bytes, UTF-8-safe) of the stderr log file associated with
// logFile — i.e. logFile with its ".log" suffix replaced by ".stderr.log".
// It returns "" if the file is missing, empty, or maxLines/maxBytes is not
// positive.
//
// The read is memory-bounded: rather than loading the whole file, it seeks
// to a suffix window sized generously enough to contain maxLines lines
// (maxBytes*4, on the assumption that lines are usually shorter than the
// byte cap, plus 64KiB of slack for line-boundary alignment) and only reads
// that window.
func ReadStderrTail(logFile string, maxLines, maxBytes int) string {
	if maxLines <= 0 || maxBytes <= 0 {
		return ""
	}
	path := strings.TrimSuffix(logFile, ".log") + ".stderr.log"

	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return ""
	}

	size := info.Size()
	window := int64(maxBytes)*4 + 64*1024
	var data []byte
	if size > window {
		if _, err := f.Seek(size-window, io.SeekStart); err != nil {
			return ""
		}
		data, err = io.ReadAll(f)
		if err != nil {
			return ""
		}
		// The seek almost certainly landed inside a line; drop that partial
		// leading line so we don't report a truncated first line as if it
		// were complete.
		if idx := bytes.IndexByte(data, '\n'); idx >= 0 {
			data = data[idx+1:]
		} else {
			data = nil
		}
	} else {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return ""
		}
		data, err = io.ReadAll(f)
		if err != nil {
			return ""
		}
	}

	text := string(data)
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	result := strings.Join(lines, "\n")

	if len(result) > maxBytes {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(result[len(result)-cut]) {
			cut--
		}
		result = result[len(result)-cut:]
	}
	return result
}
