package lifecyclehooks

import (
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
