package executor

import (
	"bytes"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// maxLineBytes caps how much of a single line lineWriter will buffer in memory.
const maxLineBytes = 64 * 1024

// truncationMarker is appended to a line that was cut off because it grew
// past maxLineBytes.
const truncationMarker = " …[truncated]"

// lineWriter is an io.Writer that splits incoming bytes on '\n' and calls emit once per complete line.
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

// Flush emits any buffered trailing partial line. It is idempotent: calling it again after the buffer has
// already been emitted (or when nothing was ever buffered) does nothing.
func (lw *lineWriter) Flush() {
	if len(lw.buf) == 0 && !lw.overLen {
		return
	}
	lw.flushLine()
}

// ReadStderrTail returns the last up-to-maxLines lines of the stderr log file associated with logFile —
// i.e. logFile with its ".log" suffix replaced by ".stderr.log".
func ReadStderrTail(logFile string, maxLines, maxBytes int) string {
	return ReadStderrTailFrom(logFile, 0, maxLines, maxBytes)
}

// ReadStderrTailFrom — как ReadStderrTail, но читает только то, что дописано начиная со смещения from.
// Лог открыт O_APPEND и общий для всех попыток стадии, поэтому для классификации нужен хвост ТОЛЬКО
// текущего запуска: иначе сетевой диагноз прошлой попытки сменил бы класс (и бюджет) следующей (см. D7
// в dev-plan.md). from=0 воспроизводит поведение ReadStderrTail (весь файл).
func ReadStderrTailFrom(logFile string, from int64, maxLines, maxBytes int) string {
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
	if from >= size {
		// За этот запуск в лог ничего не дописано (молчаливый сбой) — хвоста нет.
		return ""
	}
	if from < 0 {
		from = 0
	}

	window := int64(maxBytes)*4 + 64*1024
	start := from
	if size-window > start {
		start = size - window
	}

	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	if start > from {
		// Окно (а не смещение from) определило позицию seek'а — она почти наверняка попала в середину
		// строки, отбрасываем частичную первую строку. Сик ровно на from так не делаем: from — граница
		// записи (конец файла до текущего запуска), а не произвольная точка внутри строки.
		if idx := bytes.IndexByte(data, '\n'); idx >= 0 {
			data = data[idx+1:]
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
