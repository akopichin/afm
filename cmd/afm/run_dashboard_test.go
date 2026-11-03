package main

import (
	"bytes"
	"encoding/json"
	"net/http"
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

// The run's config reaches the dashboard: configured names label the roster and the fallback prefix is sent.
func TestStartDashboard_PassesCommandNamesToServer(t *testing.T) {
	cfg := config.Default()
	cfg.CommandNames = map[string]string{"claude": "House Claude"}
	cfg.UnknownCommandName = "test"
	baseURL := startGraphTestDashboard(t, cfg)

	var status struct {
		UnknownCommandName string `json:"unknown_command_name"`
	}
	decodeDashboardJSON(t, baseURL+"/api/status", &status)
	if status.UnknownCommandName != "test" {
		t.Errorf("unknown_command_name = %q, want test", status.UnknownCommandName)
	}

	type participant struct {
		ID          string `json:"id"`
		Command     string `json:"command"`
		CommandName string `json:"command_name"`
	}
	var roster struct {
		Participants []participant `json:"participants"`
	}
	decodeDashboardJSON(t, baseURL+"/api/mesh/participants", &roster)
	for _, p := range roster.Participants {
		if p.Command == "claude" {
			if p.CommandName != "House Claude" {
				t.Errorf("participant %s command_name = %q, want House Claude", p.ID, p.CommandName)
			}
			return
		}
	}
	t.Fatalf("no participant runs claude in %+v", roster.Participants)
}

func decodeDashboardJSON(t *testing.T, url string, into any) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // test-only, url is built from a loopback test dashboard address
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}
