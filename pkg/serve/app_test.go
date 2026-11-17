//go:build unix

package serve

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/pkg/runregistry"
)

type appEnv struct {
	opts    Options
	out     string
	project string
}

func newAppEnv(t *testing.T) *appEnv {
	t.Helper()
	e := &appEnv{out: filepath.Join(t.TempDir(), "out.jsonl"), project: t.TempDir()}
	regDir := t.TempDir()
	e.opts = Options{
		StateDir:    filepath.Join(t.TempDir(), "serve"),
		RegistryDir: regDir,
		Config: Config{
			Projects:  []Project{{ID: "app", Path: e.project}},
			Pipelines: []Pipeline{{ID: "dev", Source: "/p"}},
			Limits:    Limits{MaxRunning: 4, MaxStarting: 2},
		},
		Binary: os.Args[0],
		Env: []string{
			fakeEnv + "=1", "SERVE_FAKE_OUT=" + e.out, "SERVE_FAKE_REGISTRY=" + regDir, "SERVE_FAKE_BEHAVIOR=hold",
			"PATH=" + os.Getenv("PATH"),
		},
		Groups: AllHostGroups(),
		Port:   0,
	}
	return e
}

func (e *appEnv) open(t *testing.T) *App {
	t.Helper()
	app, err := Open(e.opts)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func serveInBackground(t *testing.T, app *App) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx) }()
	var once bool
	stop = func() error {
		if once {
			return nil
		}
		once = true
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(15 * time.Second):
			t.Fatal("Serve did not return after its context ended")
			return nil
		}
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

func TestOpenRefusesASecondHostAndNamesTheFirst(t *testing.T) {
	e := newAppEnv(t)
	first := e.open(t)
	stop := serveInBackground(t, first)
	defer func() { _ = stop() }()

	_, err := Open(e.opts)
	var locked *LockedError
	if !errors.As(err, &locked) {
		t.Fatalf("second Open error = %v, want *LockedError", err)
	}
	if locked.Info.PID != os.Getpid() || locked.Info.Endpoint != first.Endpoint() {
		t.Errorf("the refusal names %+v, want this host at %s", locked.Info, first.Endpoint())
	}
}

func TestOpenRemovesRecordsOfExitedProcessesOnly(t *testing.T) {
	e := newAppEnv(t)
	gone := exec.Command("true")
	_ = gone.Run()
	deadMarker := "1"
	liveMarker, _ := runregistry.ProcessStart(os.Getpid())
	dead := runregistry.Record{InstanceID: testInstance, PID: gone.Process.Pid, ProcessStart: deadMarker, UpdatedAt: time.Now()}
	silent := runregistry.Record{InstanceID: otherInstance, PID: os.Getpid(), ProcessStart: liveMarker, UpdatedAt: time.Now()}
	for _, rec := range []runregistry.Record{dead, silent} {
		if err := runregistry.Publish(e.opts.RegistryDir, rec); err != nil {
			t.Fatal(err)
		}
	}
	app := e.open(t)
	defer app.Close()
	records, _ := runregistry.List(e.opts.RegistryDir)
	if len(records) != 1 || records[0].InstanceID != otherInstance {
		t.Errorf("records after Open = %+v, want only the live process's", records)
	}
}

// Stopping the host must not stop what it started; a new host then finds the run and starts nothing again.
func TestShutdownLeavesRunsRunningAndARestartAdoptsThem(t *testing.T) {
	e := newAppEnv(t)
	first := e.open(t)
	stop := serveInBackground(t, first)
	receipt, err := first.launcher.Start(StartRequest{RequestID: "r1", Project: "app", Pipeline: "dev", Mode: ModeCore})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(receipt.PID, syscall.SIGKILL) })
	eventually(t, "the launch to become ready", func() bool {
		r, _ := first.host.Receipt(receipt.LaunchID)
		return r.State == StateReady
	})

	if err := stop(); err != nil {
		t.Fatalf("Serve = %v", err)
	}
	if err := syscall.Kill(receipt.PID, 0); err != nil {
		t.Fatalf("the run died with the host: %v", err)
	}
	if r, _ := first.host.Receipt(receipt.LaunchID); r.State != StateReady {
		t.Errorf("shutdown changed the receipt to %s", r.State)
	}

	second := e.open(t)
	serveInBackground(t, second)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "t", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &sdkmcp.StreamableClientTransport{Endpoint: second.Endpoint()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	body, isErr := callOn(t, cs, toolRunsList, nil)
	rows, _ := body["runs"].([]any)
	if isErr || len(rows) != 1 {
		t.Fatalf("runs after restart = %v", body)
	}
	row := rows[0].(map[string]any)
	if row["ownership"] != "managed" || row["liveness"] != "active" || row["run_id"] != "fake-1" {
		t.Errorf("the restarted host sees %v", row)
	}
	if n := len(readReports(t, e.out)); n != 1 {
		t.Errorf("children started = %d, want one: a restart must not start the run again", n)
	}
}

func TestEndpointIsPublishedInHostJSON(t *testing.T) {
	e := newAppEnv(t)
	app := e.open(t)
	defer app.Close()
	if got := readHostInfo(e.opts.StateDir); got.Endpoint != app.Endpoint() || got.PID != os.Getpid() {
		t.Errorf("host.json = %+v, endpoint %s", got, app.Endpoint())
	}
}
