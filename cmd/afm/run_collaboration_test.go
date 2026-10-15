package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
)

func writeCollabConfigYAML(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestResolveCollaboration_EnvOffBeatsFlowDepth — T1.4: AFM_COLLABORATION=0 must win even when flow.yaml
// tries to raise max_depth. A flow cannot re-enable a feature switched off by env/config.
func TestResolveCollaboration_EnvOffBeatsFlowDepth(t *testing.T) {
	t.Setenv("AFM_COLLABORATION", "0")
	five := 5
	cfg := config.Config{} // config says nothing; env wins
	f := &flow.Flow{Collaboration: &flow.FlowCollaboration{MaxDepth: &five}}
	got := resolveCollaboration(cfg, f)
	if got.Enabled {
		t.Error("Enabled = true, want false: AFM_COLLABORATION=0 must beat flow.collaboration.max_depth")
	}
}

// TestResolveCollaboration_DepthSourceReported — the resolved depth names which layer actually set it:
// flow > project > global > default.
func TestResolveCollaboration_DepthSourceReported(t *testing.T) {
	three, five, two := 3, 5, 2

	t.Run("default when nothing set", func(t *testing.T) {
		got := resolveCollaboration(config.Config{}, nil)
		if got.Depth.Source != assistants.DepthSourceDefault {
			t.Errorf("Source = %q, want %q", got.Depth.Source, assistants.DepthSourceDefault)
		}
	})

	t.Run("global layer", func(t *testing.T) {
		base := t.TempDir()
		g := writeCollabConfigYAML(t, filepath.Join(base, "g"), "collaboration:\n  max_depth: 3\n")
		cfg, err := config.LoadFrom(g, "")
		if err != nil {
			t.Fatal(err)
		}
		got := resolveCollaboration(cfg, nil)
		if got.Depth.Source != assistants.DepthSourceGlobal || got.Depth.MaxDepth != three {
			t.Errorf("got %+v, want source=global, depth=3", got.Depth)
		}
	})

	t.Run("project layer", func(t *testing.T) {
		base := t.TempDir()
		p := writeCollabConfigYAML(t, filepath.Join(base, "p"), "collaboration:\n  max_depth: 5\n")
		cfg, err := config.LoadFrom("", p)
		if err != nil {
			t.Fatal(err)
		}
		got := resolveCollaboration(cfg, nil)
		if got.Depth.Source != assistants.DepthSourceProject || got.Depth.MaxDepth != five {
			t.Errorf("got %+v, want source=project, depth=5", got.Depth)
		}
	})

	t.Run("flow layer wins over config layers", func(t *testing.T) {
		base := t.TempDir()
		g := writeCollabConfigYAML(t, filepath.Join(base, "g"), "collaboration:\n  max_depth: 3\n")
		p := writeCollabConfigYAML(t, filepath.Join(base, "p"), "collaboration:\n  max_depth: 5\n")
		cfg, err := config.LoadFrom(g, p)
		if err != nil {
			t.Fatal(err)
		}
		f := &flow.Flow{Collaboration: &flow.FlowCollaboration{MaxDepth: &two}}
		got := resolveCollaboration(cfg, f)
		if got.Depth.Source != assistants.DepthSourceFlow || got.Depth.MaxDepth != two {
			t.Errorf("got %+v, want source=flow, depth=2", got.Depth)
		}
	})
}

// TestResolveCollaboration_IndependentFromSideAgent — collaboration and side_agent are independent
// switches; resolving one must not be influenced by the other (§3.3 table).
func TestResolveCollaboration_IndependentFromSideAgent(t *testing.T) {
	tr, fl := true, false
	cases := []struct {
		name          string
		collaboration *bool
		sideAgent     *bool
		wantCollab    bool
		wantSide      bool
	}{
		{"both on", &tr, &tr, true, true},
		{"both off", &fl, &fl, false, false},
		{"collaboration on, side_agent off", &tr, &fl, true, false},
		{"collaboration off, side_agent on", &fl, &tr, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{
				Collaboration: config.CollaborationConfig{Enabled: tc.collaboration},
				SideAgent:     config.SideAgentConfig{Enabled: tc.sideAgent},
			}
			if got := resolveCollaboration(cfg, nil).Enabled; got != tc.wantCollab {
				t.Errorf("resolveCollaboration.Enabled = %v, want %v", got, tc.wantCollab)
			}
			if got := sideAgentEnabled(cfg, nil); got != tc.wantSide {
				t.Errorf("sideAgentEnabled = %v, want %v", got, tc.wantSide)
			}
		})
	}
}
