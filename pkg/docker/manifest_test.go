package docker

import (
	"testing"

	"github.com/akopichin/afm/pkg/config"
)

func TestBuildFileRootManifest_OnlyBrowseable(t *testing.T) {
	m, err := BuildFileRootManifest("/work/afm", config.ExtraMounts{
		{Path: "../shared", Name: "contracts", Browse: true},
		{Path: "~/.ai-free", Browse: false},
		{Path: "~/.legacy"}, // scalar → browse:false
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 1 {
		t.Fatalf("version=%d", m.Version)
	}
	if len(m.Roots) != 2 {
		t.Fatalf("want project+1 extra, got %d: %+v", len(m.Roots), m.Roots)
	}
	if m.Roots[0].ID != "project" || m.Roots[0].Kind != "project" || m.Roots[0].MountReadOnly {
		t.Errorf("bad project root: %+v", m.Roots[0])
	}
	if m.Roots[1].ID != "extra-1" || m.Roots[1].Label != "contracts" ||
		!m.Roots[1].MountReadOnly || m.Roots[1].ContainerPath != "/work/shared" {
		t.Errorf("bad extra root: %+v", m.Roots[1])
	}
}

func TestFileRootManifest_RoundTripAndReject(t *testing.T) {
	m := FileRootManifest{Version: 1, Roots: []FileRootManifestEntry{
		{ID: "project", Label: "afm", ContainerPath: "/work/afm", Kind: "project"},
	}}
	enc, err := EncodeFileRootManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeFileRootManifest(enc)
	if err != nil || len(got.Roots) != 1 || got.Roots[0].ContainerPath != "/work/afm" {
		t.Fatalf("round-trip: %v %+v", err, got)
	}
	if _, err := DecodeFileRootManifest("!!!not-base64!!!"); err == nil {
		t.Error("corrupt base64 must error")
	}
	bad, _ := EncodeFileRootManifest(FileRootManifest{Version: 2, Roots: m.Roots})
	if _, err := DecodeFileRootManifest(bad); err == nil {
		t.Error("bad version must error")
	}
	dup := FileRootManifest{Version: 1, Roots: []FileRootManifestEntry{
		{ID: "x", ContainerPath: "/a", Kind: "extra"},
		{ID: "x", ContainerPath: "/b", Kind: "extra"},
	}}
	de, _ := EncodeFileRootManifest(dup)
	if _, err := DecodeFileRootManifest(de); err == nil {
		t.Error("duplicate id must error")
	}
	rel := FileRootManifest{Version: 1, Roots: []FileRootManifestEntry{{ID: "y", ContainerPath: "rel/path", Kind: "extra"}}}
	re, _ := EncodeFileRootManifest(rel)
	if _, err := DecodeFileRootManifest(re); err == nil {
		t.Error("relative container path must error")
	}
}
