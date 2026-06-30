package main

import (
	"testing"
)

// TestResolveRootDir проверяет приоритет определения базовой директории:
// флаг --dir важнее переменной AFM_DIR, а та важнее текущей директории.
func TestResolveRootDir(t *testing.T) {
	tests := []struct {
		name    string
		dirFlag string
		envDir  string
		want    string
	}{
		{"флаг важнее env", "/tmp/flag", "/tmp/env", "/tmp/flag"},
		{"только env", "", "/tmp/env", "/tmp/env"},
		{"пустой env при заданном флаге", "/tmp/flag", "", "/tmp/flag"},
		{"ничего не задано — текущая директория", "", "", "."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveRootDir(tt.dirFlag, tt.envDir); got != tt.want {
				t.Errorf("resolveRootDir(%q, %q) = %q, want %q", tt.dirFlag, tt.envDir, got, tt.want)
			}
		})
	}
}

// TestFmDir проверяет, что fmDir() собирает путь к .afm поверх rootDir.
// rootDir — изменяемая глобальная переменная пакета, поэтому сохраняем и
// восстанавливаем её, чтобы не влиять на остальные тести cmd/afm,
// которые рассчитывают на значение по умолчанию ("").
func TestFmDir(t *testing.T) {
	prev := rootDir
	t.Cleanup(func() { rootDir = prev })

	cases := map[string]string{
		"":          ".afm",
		".":         ".afm",
		"/tmp/x":    "/tmp/x/.afm",
		"~/myflows": "~/myflows/.afm",
	}
	for root, want := range cases {
		rootDir = root
		if got := fmDir(); got != want {
			t.Errorf("fmDir() with rootDir=%q = %q, want %q", root, got, want)
		}
	}
}
