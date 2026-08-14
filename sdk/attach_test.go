package afmsdk

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
)

func TestAttach_ProcessDead(t *testing.T) {
	c := &Client{httpClient: http.DefaultClient}

	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	deadPID := cmd.Process.Pid

	_, err := c.Attach(context.Background(), t.TempDir(), 12345, deadPID)
	if err != ErrProcessDead {
		t.Fatalf("Attach: got %v, want ErrProcessDead", err)
	}
}

func TestAttach_PortUnreachable(t *testing.T) {
	c := &Client{httpClient: http.DefaultClient}

	_, err := c.Attach(context.Background(), t.TempDir(), 1, os.Getpid())
	if err != ErrPortUnreachable {
		t.Fatalf("Attach: got %v, want ErrPortUnreachable", err)
	}
}

func TestAttach_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"flow_name":"x","stages":[]}`))
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	c := &Client{httpClient: http.DefaultClient}

	dir := t.TempDir()
	run, err := c.Attach(context.Background(), dir, port, os.Getpid())
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if run.Dir() != dir {
		t.Errorf("Dir(): got %q, want %q", run.Dir(), dir)
	}
	if run.Port() != port {
		t.Errorf("Port(): got %d, want %d", run.Port(), port)
	}
	if run.PID() != os.Getpid() {
		t.Errorf("PID(): got %d, want %d", run.PID(), os.Getpid())
	}

	status, err := run.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.FlowName != "x" {
		t.Errorf("Status.FlowName: got %q, want \"x\"", status.FlowName)
	}
}
