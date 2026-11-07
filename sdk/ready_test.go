package afmsdk

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func shortReadyTimeout(t *testing.T) {
	t.Helper()
	old := readyTimeout
	readyTimeout = 700 * time.Millisecond
	t.Cleanup(func() { readyTimeout = old })
}

// startFailing starts the fake afm, expects Start to fail and checks the slot and workDir afterwards.
func startFailing(t *testing.T, cfg fakeCfg, mode Mode) error {
	t.Helper()
	c := useFakeAfm(t, cfg, mode)
	c.sem = make(chan struct{}, 1)
	workDir := t.TempDir()
	run, err := c.Start(context.Background(), "flow.yaml", workDir)
	if err == nil {
		_ = run.Shutdown(context.Background())
		t.Fatal("Start succeeded, want failure")
	}
	if len(c.sem) != 0 {
		t.Fatal("MaxConcurrent slot still held after Start failed")
	}
	if _, statErr := os.Stat(workDir); statErr != nil {
		t.Fatalf("workDir not preserved: %v", statErr)
	}
	return err
}

func TestStartCore_Ready(t *testing.T) {
	run := startFake(t, fakeCfg{}, ModeCore)
	if run.Mode() != ModeCore {
		t.Fatalf("Mode() = %q", run.Mode())
	}
}

func TestStartCore_Refusals(t *testing.T) {
	shortReadyTimeout(t)
	cases := map[string]struct {
		cfg          fakeCfg
		incompatible bool
	}{
		"no runtime endpoint": {fakeCfg{Runtime: "404"}, true},
		"other mode":          {fakeCfg{Runtime: "mode:dashboard"}, true},
		"other protocol":      {fakeCfg{Runtime: "protocol:2"}, true},
		"never ready":         {fakeCfg{Runtime: "notready"}, false},
		"foreign run id":      {fakeCfg{StatusRunID: "other"}, false},
		"no run directory":    {fakeCfg{NoRunDir: true}, false},
		"early exit":          {fakeCfg{ExitNow: 2}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := startFailing(t, tc.cfg, ModeCore)
			if got := errors.Is(err, ErrIncompatibleRuntime); got != tc.incompatible {
				t.Fatalf("errors.Is(ErrIncompatibleRuntime) = %v for %v", got, err)
			}
		})
	}
}

func TestStartCore_EarlyExitErrorHasOutput(t *testing.T) {
	err := startFailing(t, fakeCfg{ExitNow: 2}, ModeCore)
	if !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("error lacks afm output: %v", err)
	}
}

func TestStartCore_CancelAfterSpawnReapsProcess(t *testing.T) {
	c := useFakeAfm(t, fakeCfg{Delay: 5000}, ModeCore)
	c.sem = make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := c.Start(ctx, "flow.yaml", t.TempDir()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start = %v, want deadline exceeded", err)
	}
	if len(c.sem) != 0 {
		t.Fatal("slot held after cancelled Start")
	}
}

func TestStartDashboard_ReadyWithoutRuntimeEndpoint(t *testing.T) {
	run := startFake(t, fakeCfg{Runtime: "404"}, "")
	if run.Mode() != ModeDashboard {
		t.Fatalf("Mode() = %q", run.Mode())
	}
}

func runtimeServer(t *testing.T, body string, status int) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			_, _ = w.Write([]byte(`{"flow_name":"x","started_at":"2026-11-07T09:00:00Z","stages":[]}`))
		case "/api/runtime":
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

func TestConnectAndAttach_DetectMode(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		mode   Mode
		err    error
	}{
		{"legacy afm", "not found", 404, "", nil},
		{"core", `{"protocol_version":1,"mode":"core","run_id":"r","ready":true}`, 200, ModeCore, nil},
		{"dashboard", `{"protocol_version":1,"mode":"dashboard","run_id":"r","ready":true}`, 200, ModeDashboard, nil},
		{"unknown mode", `{"protocol_version":1,"mode":"future","run_id":"r"}`, 200, "", ErrIncompatibleRuntime},
		{"core protocol 2", `{"protocol_version":2,"mode":"core","run_id":"r"}`, 200, "", ErrIncompatibleRuntime},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			port := runtimeServer(t, tc.body, tc.status)
			run, err := Connect(context.Background(), port)
			if !errors.Is(err, tc.err) || (err == nil && run.Mode() != tc.mode) {
				t.Fatalf("Connect: mode=%v err=%v", run, err)
			}
			c := &Client{httpClient: http.DefaultClient}
			run, err = c.Attach(context.Background(), t.TempDir(), port, os.Getpid())
			if !errors.Is(err, tc.err) || (err == nil && run.Mode() != tc.mode) {
				t.Fatalf("Attach: mode=%v err=%v", run, err)
			}
		})
	}
}
