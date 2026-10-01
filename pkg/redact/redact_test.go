package redact

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriter_SecretSplitAcrossWrites(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, []string{"topsecret"})
	// секрет, разорванный между двумя Write, тоже редактируется
	if _, err := w.Write([]byte("before top")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("secret after")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "topsecret") {
		t.Fatalf("secret leaked: %q", out)
	}
	if !strings.Contains(out, DefaultMarker) {
		t.Fatalf("no redaction marker: %q", out)
	}
	if !strings.Contains(out, "before ") || !strings.Contains(out, " after") {
		t.Fatalf("non-secret text mangled: %q", out)
	}
}

func TestWriter_FullSecretOneWrite(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, []string{"topsecret"})
	if _, err := w.Write([]byte("token=topsecret\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "topsecret") {
		t.Fatalf("secret leaked: %q", out)
	}
	if !strings.Contains(out, DefaultMarker) {
		t.Fatalf("no redaction marker: %q", out)
	}
}

// TestWriter_SecretAtStartMiddleEnd — секрет в начале, середине и конце потока
// (в одном Write) редактируется во всех позициях.
func TestWriter_SecretAtStartMiddleEnd(t *testing.T) {
	for name, input := range map[string]string{
		"start":  "SEKRET tail",
		"middle": "head SEKRET tail",
		"end":    "head SEKRET",
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			w := NewWriter(&buf, []string{"SEKRET"})
			if _, err := w.Write([]byte(input)); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			out := buf.String()
			if strings.Contains(out, "SEKRET") {
				t.Fatalf("secret leaked at %s: %q", name, out)
			}
			if !strings.Contains(out, DefaultMarker) {
				t.Fatalf("no redaction marker at %s: %q", name, out)
			}
		})
	}
}

// TestWriter_MultipleOccurrences — несколько вхождений одного секрета все
// заменяются.
func TestWriter_MultipleOccurrences(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, []string{"abc"})
	if _, err := w.Write([]byte("abc-abc-abc")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "abc") {
		t.Fatalf("secret leaked: %q", out)
	}
	if n := strings.Count(out, DefaultMarker); n != 3 {
		t.Fatalf("expected 3 markers, got %d: %q", n, out)
	}
}

func TestWriter_BoundedMemoryOnLargeStream(t *testing.T) {
	var buf bytes.Buffer
	secret := "topsecret"
	w := NewWriter(&buf, []string{secret})
	// Большой поток без '\n', секрет где-то в середине: buf хвост должен
	// оставаться ограниченным (≤ maxLen-1), а секрет всё равно отредактирован.
	chunk := strings.Repeat("x", 8192)
	if _, err := w.Write([]byte(chunk)); err != nil {
		t.Fatal(err)
	}
	if len(w.buf) > len(secret)-1 {
		t.Fatalf("buf not bounded: %d bytes held", len(w.buf))
	}
	if _, err := w.Write([]byte(secret)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(chunk)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatal("secret leaked in large stream")
	}
	if !strings.Contains(out, DefaultMarker) {
		t.Fatal("no redaction marker in large stream")
	}
}

func TestMarker_FallsBackWhenSecretIsSubstringOfCandidates(t *testing.T) {
	// Секрет совпадает с содержимым маркера-кандидата → нужен безопасный фолбэк.
	secrets := []string{"REDACTED"}
	m := marker(secrets)
	if strings.Contains(m, "REDACTED") {
		t.Fatalf("marker must not contain the secret itself: %q", m)
	}
}

func TestMarker_RejectsCandidateThatIsSubstringOfAnotherSecret(t *testing.T) {
	// Секрет "a[REDACTED]b" содержит стандартный маркер как подстроку —
	// marker обязан пропустить его (условие (в), Finding #2).
	overlapping := "a[REDACTED]b"
	secrets := []string{"QR", overlapping}
	m := marker(secrets)
	if strings.Contains(overlapping, m) && m != "" {
		t.Fatalf("marker must not be a substring of another secret: %q", m)
	}
	if m == DefaultMarker {
		t.Fatalf("expected marker to skip the default marker, got %q", m)
	}
}

// TestWriter_NoSynthesisAcrossFlushedBoundary воспроизводит утечку из
// Finding #2: секрет "QR" редактируется на границе двух Write так, что
// сброшенный в лог контекст "a" + маркер + "b" совпадает со значением
// ДРУГОГО секрета "a[REDACTED]b". Правильный маркер должен исключать такое
// совпадение — итоговый лог не должен содержать значение второго секрета
// целиком.
func TestWriter_NoSynthesisAcrossFlushedBoundary(t *testing.T) {
	shortSecret := "QR"
	overlapping := "a[REDACTED]b" // содержит дефолтный маркер как подстроку
	var buf bytes.Buffer
	w := NewWriter(&buf, []string{shortSecret, overlapping})
	if w.marker == DefaultMarker {
		t.Fatalf("Writer must not pick a marker that is a substring of overlapping, got %q", w.marker)
	}
	// "aQ" flush-ит "a" (Q удержан как возможный префикс shortSecret), затем
	// "Rb" достраивает "QR" -> marker, стыкуясь с уже сброшенным "a" и
	// последующим "b" — именно граница, которую пропускала старая проверка.
	if _, err := w.Write([]byte("aQ")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("Rb")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, shortSecret) {
		t.Fatalf("shortSecret leaked: %q", out)
	}
	if strings.Contains(out, overlapping) {
		t.Fatalf("overlapping secret synthesized across flushed Write boundary: %q", out)
	}
}

// TestWriter_OverlappingSecrets — перекрывающиеся секреты ("ab" ⊂ "abc"):
// длинный матчится первым (сортировка по убыванию длины), а fixed-point проход
// добивает остатки — ни один секрет не утекает.
func TestWriter_OverlappingSecrets(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, []string{"ab", "abc"})
	if _, err := w.Write([]byte("xabcy and xaby")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "abc") || strings.Contains(out, "ab") {
		t.Fatalf("overlapping secret leaked: %q", out)
	}
}

func TestString(t *testing.T) {
	if got := String("no secrets here", nil); got != "no secrets here" {
		t.Fatalf("no secrets: no-op expected, got %q", got)
	}
	if got := String("still unchanged", []string{}); got != "still unchanged" {
		t.Fatalf("empty secrets: no-op expected, got %q", got)
	}
	got := String("error: token s3cr3t failed", []string{"s3cr3t"})
	if strings.Contains(got, "s3cr3t") {
		t.Fatalf("secret leaked in error string: %q", got)
	}
	if !strings.Contains(got, DefaultMarker) {
		t.Fatalf("expected redaction marker: %q", got)
	}
}
