package main

import (
	"bytes"
	"strings"
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

// TestRootVersionOutput проверяет, что корневая команда печатает версию по --version.
// cobra регистрирует --version только если cmd.Version != "".
func TestRootVersionOutput(t *testing.T) {
	root := newRootCmd()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"--version"})

	if err := root.Execute(); err != nil {
		t.Fatalf("--version execute: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, version) {
		t.Errorf("--version output=%q, want it to contain %q", got, version)
	}
}

// TestResolveDebug проверяет приоритет определения debug-режима:
// флаг --debug важнее переменной AFM_DEBUG.
func TestResolveDebug(t *testing.T) {
	cases := []struct {
		flag bool
		env  string
		want bool
	}{
		{true, "", true},
		{true, "0", true}, // флаг важнее env
		{false, "1", true},
		{false, "true", true},
		{false, "ON", true},
		{false, "", false},
		{false, "nope", false},
	}
	for _, c := range cases {
		if got := resolveDebug(c.flag, c.env); got != c.want {
			t.Errorf("resolveDebug(%v,%q)=%v want %v", c.flag, c.env, got, c.want)
		}
	}
}
