package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
)

func TestUsageLimit_Defaults(t *testing.T) {
	cfg, err := config.LoadFrom(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	u := cfg.UsageLimit
	if got := u.ResolvedResumeMargin(); got != time.Minute {
		t.Errorf("resume_margin = %v, want 1m", got)
	}
	if got := u.ResolvedProbeInterval(); got != 15*time.Minute {
		t.Errorf("probe_interval = %v, want 15m", got)
	}
	if got := u.ResolvedMaxWait(); got != 12*time.Hour {
		t.Errorf("max_wait = %v, want 12h", got)
	}
	if got := u.ResolvedMaxWaits(); got != 4 {
		t.Errorf("max_waits = %d, want 4", got)
	}
}

func TestUsageLimit_ProjectOverridesGlobalPerField(t *testing.T) {
	globalDir, projectDir := t.TempDir(), t.TempDir()
	writeYAML(t, globalDir, "config.yaml", `
usage_limit:
  resume_margin: 2m
  probe_interval: 5m
  max_wait: 6h
  max_waits: 2
`)
	writeYAML(t, projectDir, "config.yaml", `
usage_limit:
  probe_interval: 30s
  max_waits: 9
`)
	cfg, err := config.LoadFrom(globalDir, projectDir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	u := cfg.UsageLimit
	if got := u.ResolvedResumeMargin(); got != 2*time.Minute {
		t.Errorf("resume_margin = %v, want global 2m", got)
	}
	if got := u.ResolvedProbeInterval(); got != 30*time.Second {
		t.Errorf("probe_interval = %v, want project 30s", got)
	}
	if got := u.ResolvedMaxWait(); got != 6*time.Hour {
		t.Errorf("max_wait = %v, want global 6h", got)
	}
	if got := u.ResolvedMaxWaits(); got != 9 {
		t.Errorf("max_waits = %d, want project 9", got)
	}
}

func TestUsageLimit_RejectsNonPositiveValues(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"zero resume_margin", "resume_margin: 0s", "usage_limit.resume_margin"},
		{"negative resume_margin", "resume_margin: -1m", "usage_limit.resume_margin"},
		{"zero probe_interval", "probe_interval: 0s", "usage_limit.probe_interval"},
		{"negative probe_interval", "probe_interval: -5m", "usage_limit.probe_interval"},
		{"zero max_wait", "max_wait: 0s", "usage_limit.max_wait"},
		{"negative max_wait", "max_wait: -1h", "usage_limit.max_wait"},
		{"zero max_waits", "max_waits: 0", "usage_limit.max_waits"},
		{"negative max_waits", "max_waits: -3", "usage_limit.max_waits"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projectDir := t.TempDir()
			writeYAML(t, projectDir, "config.yaml", "usage_limit:\n  "+tc.yaml+"\n")
			_, err := config.LoadFrom(t.TempDir(), projectDir)
			if err == nil {
				t.Fatal("LoadFrom accepted a non-positive value")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %s", err, tc.want)
			}
		})
	}
}
