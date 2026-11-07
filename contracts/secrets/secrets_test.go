package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRef_EnvAndFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "tok")
	if err := os.WriteFile(fp, []byte("  file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if v, err := ResolveRef("file:"+fp, nil); err != nil || v != "file-secret" {
		t.Fatalf("file: got %q err %v", v, err)
	}
	loaded := map[string]string{"K": "map-secret"}
	if v, _ := ResolveRef("env:K", loaded); v != "map-secret" {
		t.Fatalf("env from map: %q", v)
	}
	t.Setenv("K2", "proc-secret")
	if v, _ := ResolveRef("env:K2", nil); v != "proc-secret" {
		t.Fatalf("env from process: %q", v)
	}
	if _, err := ResolveRef("env:MISSING", nil); err == nil {
		t.Fatal("missing env must error")
	}
	if _, err := ResolveRef("plain", nil); err == nil {
		t.Fatal("unprefixed must error")
	}
	if _, err := ResolveRef("file:"+filepath.Join(dir, "nope"), nil); err == nil {
		t.Fatal("missing file must error")
	}
}

func TestExpandHome(t *testing.T) {
	if got := ExpandHome("~/x", "/home/u"); got != "/home/u/x" {
		t.Fatalf("got %q", got)
	}
	if got := ExpandHome("/abs", "/home/u"); got != "/abs" {
		t.Fatalf("abs changed: %q", got)
	}
	if got := ExpandHome("~", "/home/u"); got != "/home/u" {
		t.Fatalf("bare tilde: %q", got)
	}
	if got := ExpandHome("~user/x", "/home/u"); got != "~user/x" {
		t.Fatalf("~user must not expand: %q", got)
	}
}

func TestLoadSecrets_MergesLayers(t *testing.T) {
	d1 := t.TempDir()
	d2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(d1, "a.env"), []byte("TOKEN=global\nSHARED=g\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d2, "b.env"), []byte("SHARED=project\nOTHER=p\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := LoadSecrets([]string{filepath.Join(d1, "a.env"), filepath.Join(d2, "b.env")})
	if err != nil {
		t.Fatal(err)
	}
	if m["TOKEN"] != "global" || m["OTHER"] != "p" || m["SHARED"] != "project" {
		t.Errorf("merge: %#v", m)
	}
}

func TestLoadSecrets_MissingFileIgnored(t *testing.T) {
	m, err := LoadSecrets([]string{filepath.Join(t.TempDir(), "nope.env")})
	if err != nil {
		t.Fatalf("missing file should be ignored: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %#v", m)
	}
}
