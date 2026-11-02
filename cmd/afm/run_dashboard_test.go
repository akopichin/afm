package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
)

var ansiSequence = regexp.MustCompile("\033\\[[0-9;]*m")

func TestPrintDashboardURL(t *testing.T) {
	const url = "http://localhost:8001"
	const plainBlock = "\n  ➜  dashboard: http://localhost:8001\n\n"
	const plainBlockWithHint = "\n  ➜  dashboard: http://localhost:8001\n" +
		"     open this URL in your browser to follow the run\n\n"

	tests := []struct {
		name  string
		hint  bool
		color bool
		plain string
	}{
		{"plain without hint", false, false, plainBlock},
		{"plain with hint", true, false, plainBlockWithHint},
		{"color without hint", false, true, plainBlock},
		{"color with hint", true, true, plainBlockWithHint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			printDashboardURL(&buf, url, tt.hint, tt.color)
			got := buf.String()

			if !tt.color {
				if got != tt.plain {
					t.Fatalf("output = %q, want %q", got, tt.plain)
				}
				return
			}
			if stripped := ansiSequence.ReplaceAllString(got, ""); stripped != tt.plain {
				t.Fatalf("output without ANSI = %q, want %q", stripped, tt.plain)
			}
			wantStyled := []string{
				colorGreen + "➜" + colorReset,
				colorBold + "dashboard:" + colorReset,
				colorBold + colorCyan + url + colorReset,
			}
			if tt.hint {
				wantStyled = append(wantStyled, colorGray+"open this URL in your browser to follow the run"+colorReset)
			}
			for _, want := range wantStyled {
				if !strings.Contains(got, want) {
					t.Errorf("colored output %q lacks %q", got, want)
				}
			}
		})
	}
}

func TestDashboardColorEnabled(t *testing.T) {
	t.Run("NO_COLOR set", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		if dashboardColorEnabled() {
			t.Fatal("colour enabled although NO_COLOR is set")
		}
	})
	t.Run("stdout is a pipe", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		captureStdout(t, func() {
			if dashboardColorEnabled() {
				t.Error("colour enabled although stdout is not a terminal")
			}
		})
	})
}

// TestStartDashboard_HideAttachHelpersInStatus: startDashboard resolves config and env once and the result reaches
// /api/status; an explicit env overrides the config value in both directions.
func TestStartDashboard_HideAttachHelpersInStatus(t *testing.T) {
	hide, show := true, false
	tests := []struct {
		name   string
		env    string
		config *bool
		want   bool
	}{
		{name: "default", want: false},
		{name: "config true", config: &hide, want: true},
		{name: "config false", config: &show, want: false},
		{name: "env 1 over config false", env: "1", config: &show, want: true},
		{name: "env 0 over config true", env: "0", config: &hide, want: false},
		{name: "empty env keeps config true", env: "", config: &hide, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AFM_HIDE_ATTACH_HELPERS", tt.env)
			cfg := config.Default()
			cfg.Collaboration.HideAttachHelpers = tt.config
			base := startGraphTestDashboard(t, cfg)

			code, body := getBody(t, base+"/api/status")
			if code != 200 {
				t.Fatalf("GET /api/status = %d: %s", code, body)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body), &raw); err != nil {
				t.Fatal(err)
			}
			got, ok := raw["hide_attach_helpers"]
			if !ok {
				t.Fatalf("hide_attach_helpers missing from /api/status: %s", body)
			}
			if want := map[bool]string{true: "true", false: "false"}[tt.want]; string(got) != want {
				t.Errorf("hide_attach_helpers = %s, want %s", got, want)
			}
		})
	}
}
