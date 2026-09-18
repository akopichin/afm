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

func TestTransportName_InjectiveByIndex(t *testing.T) {
	// Разные (hookIdx,varIdx) → разные имена; одинаковые (hookIdx,varIdx) →
	// одно и то же имя (детерминированность, не только уникальность).
	names := map[string]bool{}
	for hookIdx := 0; hookIdx < 3; hookIdx++ {
		for varIdx := 0; varIdx < 3; varIdx++ {
			n := TransportName(hookIdx, varIdx)
			if names[n] {
				t.Fatalf("collision on TransportName(%d,%d) = %q", hookIdx, varIdx, n)
			}
			names[n] = true
		}
	}
	if got := TransportName(1, 2); got != TransportName(1, 2) {
		t.Fatalf("TransportName must be deterministic: %q != %q", got, TransportName(1, 2))
	}
	if !strings.HasPrefix(TransportName(0, 0), HookSecretTransportPrefix) {
		t.Fatalf("TransportName must carry the shared prefix: %q", TransportName(0, 0))
	}
}

func TestSortedEnvKeys_Deterministic(t *testing.T) {
	h := Hook{Env: map[string]SecretRef{"C": "env:C", "A": "env:A", "B": "env:B"}}
	got := SortedEnvKeys(h)
	want := []string{"A", "B", "C"}
	if len(got) != len(want) {
		t.Fatalf("SortedEnvKeys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SortedEnvKeys = %v, want %v", got, want)
		}
	}
}

func TestResolveHookEnvFromTransport(t *testing.T) {
	h := Hook{ID: "tg", Env: map[string]SecretRef{
		"TELEGRAM_BOT_TOKEN": "env:TELEGRAM", // ref сам НЕ используется в this path
		"TELEGRAM_CHAT_ID":   "env:CHAT",
	}}
	// hookIdx=2 — произвольная позиция, ровно как это было бы в combined.
	keys := SortedEnvKeys(h) // [TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID]
	t.Setenv(TransportName(2, 0), "bot-token")
	t.Setenv(TransportName(2, 1), "-100500")

	got, err := ResolveHookEnvFromTransport(2, h)
	if err != nil {
		t.Fatal(err)
	}
	if got[keys[0]] != "bot-token" || got[keys[1]] != "-100500" {
		t.Fatalf("resolved from transport: %v", got)
	}
}

func TestResolveHookEnvFromTransport_MissingIsError(t *testing.T) {
	h := Hook{ID: "tg", Env: map[string]SecretRef{"X": "env:X"}}
	_, err := ResolveHookEnvFromTransport(0, h)
	if err == nil {
		t.Fatal("missing transport var must fail-fast")
	}
	if !strings.Contains(err.Error(), "tg") || !strings.Contains(err.Error(), "X") {
		t.Fatalf("error must name hook id and var: %v", err)
	}
}

func TestResolveHookEnvFromTransport_EmptyIsError(t *testing.T) {
	h := Hook{ID: "tg", Env: map[string]SecretRef{"X": "env:X"}}
	t.Setenv(TransportName(0, 0), "") // выставлена, но пуста
	_, err := ResolveHookEnvFromTransport(0, h)
	if err == nil {
		t.Fatal("empty transport var must fail-fast, not silently resolve to \"\"")
	}
}

func TestResolveHookEnvFromTransport_EmptyNil(t *testing.T) {
	got, err := ResolveHookEnvFromTransport(0, Hook{ID: "h"})
	if err != nil || got != nil {
		t.Fatalf("empty env → nil,nil; got %v %v", got, err)
	}
}

func TestUnsetTransportVars_RemovesOnlyHookSecretPrefix(t *testing.T) {
	t.Setenv(TransportName(0, 0), "v0")
	t.Setenv(TransportName(1, 3), "v1")
	t.Setenv("AFM_SECRET_GLM51", "unrelated-autoshim-secret") // must survive
	t.Setenv("SOME_OTHER_VAR", "unrelated")                   // must survive

	UnsetTransportVars()

	if v := os.Getenv(TransportName(0, 0)); v != "" {
		t.Fatalf("transport var not unset: %q", v)
	}
	if v := os.Getenv(TransportName(1, 3)); v != "" {
		t.Fatalf("transport var not unset: %q", v)
	}
	if v := os.Getenv("AFM_SECRET_GLM51"); v != "unrelated-autoshim-secret" {
		t.Fatalf("UnsetTransportVars must not touch autoShim AFM_SECRET_*: %q", v)
	}
	if v := os.Getenv("SOME_OTHER_VAR"); v != "unrelated" {
		t.Fatalf("UnsetTransportVars must not touch unrelated env: %q", v)
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
	if !strings.Contains(out, defaultRedactionMarker) {
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
	if !strings.Contains(out, defaultRedactionMarker) {
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
	if !strings.Contains(out, defaultRedactionMarker) {
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

func TestRedactMarker_RejectsCandidateThatIsSubstringOfAnotherSecret(t *testing.T) {
	// Секрет "a[REDACTED]b" содержит стандартный маркер как подстроку —
	// redactMarker обязан пропустить его (условие (в), Finding #2).
	overlapping := "a[REDACTED]b"
	secrets := []string{"QR", overlapping}
	m := redactMarker(secrets)
	if strings.Contains(overlapping, m) && m != "" {
		t.Fatalf("marker must not be a substring of another secret: %q", m)
	}
	if m == defaultRedactionMarker {
		t.Fatalf("expected redactMarker to skip the default marker, got %q", m)
	}
}

// TestRedactingWriter_NoSynthesisAcrossFlushedBoundary воспроизводит утечку
// из Finding #2: секрет "QR" редактируется на границе двух Write так, что
// сброшенный в лог контекст "a" + маркер + "b" совпадает со значением
// ДРУГОГО секрета "a[REDACTED]b". Правильный маркер должен исключать такое
// совпадение — итоговый лог не должен содержать значение второго секрета
// целиком.
func TestRedactingWriter_NoSynthesisAcrossFlushedBoundary(t *testing.T) {
	shortSecret := "QR"
	overlapping := "a[REDACTED]b" // содержит дефолтный маркер как подстроку
	var buf bytes.Buffer
	w := newRedactingWriter(&buf, []string{shortSecret, overlapping})
	if w.marker == defaultRedactionMarker {
		t.Fatalf("redactingWriter must not pick a marker that is a substring of overlapping, got %q", w.marker)
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

func TestRedactString(t *testing.T) {
	if got := redactString("no secrets here", nil); got != "no secrets here" {
		t.Fatalf("no secrets: no-op expected, got %q", got)
	}
	got := redactString("error: token s3cr3t failed", []string{"s3cr3t"})
	if strings.Contains(got, "s3cr3t") {
		t.Fatalf("secret leaked in error string: %q", got)
	}
	if !strings.Contains(got, defaultRedactionMarker) {
		t.Fatalf("expected redaction marker: %q", got)
	}
}
