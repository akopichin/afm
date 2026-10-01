package lifecyclehooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/redact"
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

// TestSecretValues_FeedsRedactor — регрессия на Hook-специфичный glue после
// выноса примитива в pkg/redact: secretValues извлекает значения ResolvedEnv, а
// пропущенная через redact строка их маскирует (поведение lifecyclehooks не
// изменилось). Байт-в-байт семантику самого редактора покрывает pkg/redact.
func TestSecretValues_FeedsRedactor(t *testing.T) {
	h := Hook{ResolvedEnv: map[string]string{"A": "s3cr3t", "B": "topsecret"}}
	vals := secretValues(h)
	if len(vals) != 2 {
		t.Fatalf("secretValues: expected 2 values, got %d: %v", len(vals), vals)
	}
	got := redact.String("A=s3cr3t B=topsecret", vals)
	if strings.Contains(got, "s3cr3t") || strings.Contains(got, "topsecret") {
		t.Fatalf("secret leaked through lifecyclehooks glue: %q", got)
	}
	if !strings.Contains(got, redact.DefaultMarker) {
		t.Fatalf("expected redaction marker: %q", got)
	}
}
