package lifecyclehooks

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveHookEnv(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "chat")
	if err := os.WriteFile(fp, []byte("-100500\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := map[string]string{"TELEGRAM": "bot-token"}
	h := Hook{ID: "tg", Env: map[string]SecretRef{
		"TELEGRAM_BOT_TOKEN": "env:TELEGRAM",
		"TELEGRAM_CHAT_ID":   SecretRef("file:" + fp),
	}}
	got, err := ResolveHookEnv(h, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if got["TELEGRAM_BOT_TOKEN"] != "bot-token" || got["TELEGRAM_CHAT_ID"] != "-100500" {
		t.Fatalf("resolved: %v", got)
	}
}

func TestResolveHookEnv_MissingIsError(t *testing.T) {
	h := Hook{ID: "tg", Env: map[string]SecretRef{"X": "env:NOPE_MISSING"}}
	_, err := ResolveHookEnv(h, nil)
	if err == nil {
		t.Fatal("missing secret must fail-fast")
	}
	if !strings.Contains(err.Error(), "tg") || !strings.Contains(err.Error(), "X") {
		t.Fatalf("error must name hook id and var: %v", err)
	}
	// значение секрета НЕ должно попадать в текст ошибки — источник может быть в проце
}

func TestResolveHookEnv_EmptyNil(t *testing.T) {
	got, err := ResolveHookEnv(Hook{ID: "h"}, nil)
	if err != nil || got != nil {
		t.Fatalf("empty env → nil,nil; got %v %v", got, err)
	}
}

func TestRedactingWriter(t *testing.T) {
	var buf bytes.Buffer
	w := newRedactingWriter(&buf, []string{"topsecret"})
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
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("no redaction marker: %q", out)
	}
	if !strings.Contains(out, "before ") || !strings.Contains(out, " after") {
		t.Fatalf("non-secret text mangled: %q", out)
	}
}

func TestRedactingWriter_FullSecretOneWrite(t *testing.T) {
	var buf bytes.Buffer
	w := newRedactingWriter(&buf, []string{"topsecret"})
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
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("no redaction marker: %q", out)
	}
}

func TestRedactingWriter_BoundedMemoryOnLargeStream(t *testing.T) {
	var buf bytes.Buffer
	secret := "topsecret"
	w := newRedactingWriter(&buf, []string{secret})
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
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatal("no redaction marker in large stream")
	}
}

func TestRedactMarker_FallsBackWhenSecretIsSubstringOfCandidates(t *testing.T) {
	// Секрет совпадает с содержимым маркера-кандидата → нужен безопасный фолбэк.
	secrets := []string{"REDACTED"}
	m := redactMarker(secrets)
	if strings.Contains(m, "REDACTED") {
		t.Fatalf("marker must not contain the secret itself: %q", m)
	}
}

func TestRedactString(t *testing.T) {
	if got := redactString("no secrets here", nil); got != "no secrets here" {
		t.Fatalf("no secrets: no-op expected, got %q", got)
	}
	got := redactString("error: token s3cr3t failed", []string{"s3cr3t"})
	if strings.Contains(got, "s3cr3t") {
		t.Fatalf("secret leaked in error string: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected redaction marker: %q", got)
	}
}
