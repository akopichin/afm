//go:build unix

package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/runregistry"
)

// A Location that net/http fails to parse is quoted, credentials and all, in the transport's own error.
const (
	leakSecret   = "synthetic-secret-7f3a"
	leakNonce    = "nonce-91c2"
	leakLocation = "http://user:" + leakSecret + "@127.0.0.1:invalid/?token=" + leakNonce
)

func leaks(text string) bool {
	return strings.Contains(text, leakSecret) || strings.Contains(text, leakNonce)
}

// redirectingRun answers /api/runtime as instance (or redirects it too when instance is empty) and redirects
// every other path to a malformed, credential-bearing Location.
func redirectingRun(t *testing.T, instance string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/runtime" && instance != "" {
			_ = json.NewEncoder(w).Encode(Runtime{ProtocolVersion: 1, Mode: "core", RunID: "run-1", Ready: true, InstanceID: instance})
			return
		}
		w.Header().Set("Location", leakLocation)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeRuntimeHidesTransportErrors(t *testing.T) {
	srv := redirectingRun(t, "")
	_, err := ProbeRuntime(context.Background(), srv.URL)
	var remote *RemoteError
	if !errors.As(err, &remote) || leaks(err.Error()) {
		t.Fatalf("probe error = %v, want a *RemoteError without the Location", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ProbeRuntime(ctx, srv.URL)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "cancelled") || strings.Contains(err.Error(), srv.URL) {
		t.Errorf("cancelled probe = %v, want context.Canceled behind a fixed message", err)
	}
}

func TestAttachHidesTransportErrors(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	srv := redirectingRun(t, "")
	body, isErr := f.call(toolAttach, map[string]any{"endpoint": srv.URL})
	raw, _ := json.Marshal(body)
	if !isErr || leaks(string(raw)) {
		t.Errorf("attach result = %s", raw)
	}
}

// The endpoint may answer /api/runtime correctly and fail only when the MCP session is opened.
func TestSessionFailuresHideTransportErrors(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	instance := InstanceIDFor(LaunchID("req"))
	srv := redirectingRun(t, instance)
	if _, _, err := f.host.CreateReceipt("req", baseParams, LaunchIdentity{}, now0); err != nil {
		t.Fatal(err)
	}
	marker, _ := runregistry.ProcessStart(os.Getpid())
	if err := runregistry.Publish(f.regDir, runregistry.Record{
		InstanceID: instance, LaunchID: LaunchID("req"), PID: os.Getpid(), ProcessStart: marker, APIURL: srv.URL,
		RunID: "run-1", Ready: true, StartedAt: now0, UpdatedAt: now0,
	}); err != nil {
		t.Fatal(err)
	}

	_, _, err := f.sessions.Status(context.Background(), RunView{RunID: "run-1", InstanceID: instance, Endpoint: srv.URL})
	var remote *RemoteError
	if !errors.As(err, &remote) || leaks(err.Error()) {
		t.Errorf("status error = %v, want a *RemoteError without the Location", err)
	}
	body, _ := f.call(toolRunsList, nil)
	if raw, _ := json.Marshal(body); leaks(string(raw)) {
		t.Errorf("runs list = %s", raw)
	}
	body, _ = f.call("afm_run_status", map[string]any{"run_id": "run-1"})
	if raw, _ := json.Marshal(body); leaks(string(raw)) {
		t.Errorf("forwarded status = %s", raw)
	}
}
