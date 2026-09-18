package docker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/docker"
)

func TestResolveSystemPrompt(t *testing.T) {
	spFile := filepath.Join(t.TempDir(), "sp.md")
	if err := os.WriteFile(spFile, []byte("you are glm"), 0600); err != nil {
		t.Fatal(err)
	}
	if v, err := docker.ResolveSystemPrompt("file:" + spFile); err != nil || v != "you are glm" {
		t.Errorf("got v=%q err=%v", v, err)
	}
	if v, err := docker.ResolveSystemPrompt(""); err != nil || v != "" {
		t.Errorf("empty ref: v=%q err=%v", v, err)
	}
	if _, err := docker.ResolveSystemPrompt("file:" + filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("missing sysprompt should error")
	}
	if _, err := docker.ResolveSystemPrompt("env:X"); err == nil {
		t.Error("non-file: ref should error")
	}
}

func TestLoadSecretLayers_DefaultAndOverride(t *testing.T) {
	// project layer переопределяет. MkdirAll нужен: os.WriteFile не создаёт
	// родительские каталоги (.afm), без него файл не запишется.
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".afm"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".afm", "secrets.env"), []byte("K=project\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := docker.LoadSecretLayers("", proj)
	if err != nil {
		t.Fatal(err)
	}
	if m["K"] != "project" {
		t.Errorf("default project layer: %#v", m)
	}
	// override
	ov := filepath.Join(t.TempDir(), "ov.env")
	if err := os.WriteFile(ov, []byte("K=override\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m2, _ := docker.LoadSecretLayers(ov, proj)
	if m2["K"] != "override" {
		t.Errorf("override layer: %#v", m2)
	}
}
