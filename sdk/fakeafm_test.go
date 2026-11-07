package afmsdk

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// fakeCfg drives the stand-in afm: the test binary re-executes itself with AFM_FAKE_CFG set.
type fakeCfg struct {
	Runtime      string `json:"runtime"`       // "", "404", "notready", "mode:x", "protocol:n"
	RunID        string `json:"run_id"`        // default "run-1"
	RunStatus    string `json:"run_status"`    // default "running"
	Ended        bool   `json:"ended"`         // sets ended_at
	StatusRunID  string `json:"status_run_id"` // /api/status run_id when it differs from /api/runtime
	NoRunDir     bool   `json:"no_run_dir"`    // skip <dir>/.afm/runs/<run_id>
	IgnoreSigint bool   `json:"ignore_sigint"`
	ExitCode     int    `json:"exit_code"`     // code on SIGINT
	Orphan       bool   `json:"orphan"`        // leave a child holding the pipes, then exit 0
	OrphanPIDs   string `json:"orphan_pids"`   // file the orphan child writes its pid to
	FloodBytes   int    `json:"flood_bytes"`   // write to stderr, then exit 3
	ExitNow      int    `json:"exit_now"`      // print a line and exit with this code at once
	Delay        int    `json:"delay_ms"`      // sleep before serving
	DieBySignal  bool   `json:"die_by_signal"` // on SIGINT die of SIGTERM instead of exiting
}

const fakeEnv = "AFM_FAKE_CFG"

// init runs before the test runner: when re-executed as the fake afm, it never reaches the tests.
func init() {
	raw := os.Getenv(fakeEnv)
	if raw == "" {
		return
	}
	var cfg fakeCfg
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "fake afm: bad cfg:", err)
		os.Exit(99)
	}
	os.Exit(runFakeAfm(cfg))
}

// runFakeAfm serves the fake afm and returns the process exit code.
func runFakeAfm(cfg fakeCfg) int {
	if pidFile := os.Getenv("AFM_FAKE_ORPHAN_CHILD"); pidFile != "" {
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
		time.Sleep(30 * time.Second)
		return 0
	}
	dir, port := "", 0
	args := os.Args[1:]
	for i, a := range args {
		switch a {
		case "--dir":
			dir = args[i+1]
		case "--port":
			port, _ = strconv.Atoi(args[i+1])
		default:
		}
	}
	if cfg.ExitNow != 0 {
		fmt.Fprintln(os.Stderr, "fake afm: refusing to start")
		return cfg.ExitNow
	}
	if cfg.FloodBytes > 0 {
		chunk := make([]byte, 1<<10)
		for i := range chunk {
			chunk[i] = 'x'
		}
		for n := 0; n < cfg.FloodBytes; n += len(chunk) {
			_, _ = os.Stderr.Write(chunk)
		}
		return 3
	}
	time.Sleep(time.Duration(cfg.Delay) * time.Millisecond)

	runID := cfg.RunID
	if runID == "" {
		runID = "run-1"
	}
	if !cfg.NoRunDir {
		_ = os.MkdirAll(filepath.Join(dir, ".afm", "runs", runID), 0o755)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		status := cfg.RunStatus
		if status == "" {
			status = "running"
		}
		ended := ""
		if cfg.Ended {
			ended = `,"ended_at":"2026-11-07T10:00:00Z"`
		}
		statusRunID := runID
		if cfg.StatusRunID != "" {
			statusRunID = cfg.StatusRunID
		}
		_, _ = fmt.Fprintf(w, `{"flow_name":"demo","run_id":%q,"started_at":"2026-11-07T09:00:00Z","run_status":%q%s,"stages":[]}`, statusRunID, status, ended)
	})
	mux.HandleFunc("/api/runtime", func(w http.ResponseWriter, r *http.Request) {
		mode, proto, ready := "core", 1, true
		switch {
		case cfg.Runtime == "404":
			http.NotFound(w, r)
			return
		case cfg.Runtime == "notready":
			ready = false
		case len(cfg.Runtime) > 5 && cfg.Runtime[:5] == "mode:":
			mode = cfg.Runtime[5:]
		case len(cfg.Runtime) > 9 && cfg.Runtime[:9] == "protocol:":
			proto, _ = strconv.Atoi(cfg.Runtime[9:])
		default:
		}
		_, _ = fmt.Fprintf(w, `{"protocol_version":%d,"mode":%q,"run_id":%q,"ready":%t}`, proto, mode, runID, ready)
	})
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT)
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.ListenAndServe() }()

	for range sigs {
		if cfg.IgnoreSigint {
			continue
		}
		if cfg.Orphan {
			child := exec.Command(os.Args[0]) //nolint:gosec // the fake re-executes its own test binary
			child.Env = append(os.Environ(), "AFM_FAKE_ORPHAN_CHILD="+cfg.OrphanPIDs)
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			_ = child.Start()
		}
		if cfg.DieBySignal {
			// Default disposition makes the runtime re-raise SIGTERM, so the wait status is a signal death.
			signal.Reset(syscall.SIGTERM)
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
			select {}
		}
		return cfg.ExitCode
	}
	return 0
}

// useFakeAfm points a new Client at this test binary acting as afm.
func useFakeAfm(t *testing.T, cfg fakeCfg, mode Mode) *Client {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeEnv, string(raw))
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{Binary: exe, Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
