package docker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/docker"
)

func TestLoadSecrets_MergesLayers(t *testing.T) {
	d1 := t.TempDir()
	d2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(d1, "a.env"), []byte("TOKEN=global\nSHARED=g\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d2, "b.env"), []byte("SHARED=project\nOTHER=p\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := docker.LoadSecrets([]string{filepath.Join(d1, "a.env"), filepath.Join(d2, "b.env")})
	if err != nil {
		t.Fatal(err)
	}
	if m["TOKEN"] != "global" || m["OTHER"] != "p" || m["SHARED"] != "project" {
		t.Errorf("merge: %#v", m)
	}
}

func TestLoadSecrets_MissingFileIgnored(t *testing.T) {
	m, err := docker.LoadSecrets([]string{filepath.Join(t.TempDir(), "nope.env")})
	if err != nil {
		t.Fatalf("missing file should be ignored: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %#v", m)
	}
}

func TestResolveAuthValue(t *testing.T) {
	tokFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokFile, []byte("abc123\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// env: form — из map
	if v, err := docker.ResolveAuthValue("env:TOKEN", map[string]string{"TOKEN": "from-map"}); err != nil || v != "from-map" {
		t.Errorf("env:map: v=%q err=%v", v, err)
	}
	// env: form — fallback на os.Getenv
	t.Setenv("MYENV", "from-os")
	if v, err := docker.ResolveAuthValue("env:MYENV", nil); err != nil || v != "from-os" {
		t.Errorf("env:os: v=%q err=%v", v, err)
	}
	// env: form — отсутствует
	if _, err := docker.ResolveAuthValue("env:NOPE_NOPE", nil); err == nil {
		t.Error("missing env should error")
	}
	// file: form
	if v, err := docker.ResolveAuthValue("file:"+tokFile, nil); err != nil || v != "abc123" {
		t.Errorf("file: v=%q err=%v", v, err)
	}
	// file: missing
	if _, err := docker.ResolveAuthValue("file:"+filepath.Join(t.TempDir(), "nope"), nil); err == nil {
		t.Error("missing file should error")
	}
}

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
