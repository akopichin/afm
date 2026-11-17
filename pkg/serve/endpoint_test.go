package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseLoopbackURL(t *testing.T) {
	tests := []struct {
		in, want, wantErr string
	}{
		{in: "http://127.0.0.1:8080", want: "http://127.0.0.1:8080"},
		{in: "http://localhost:8080/mcp", want: "http://localhost:8080/mcp"},
		{in: "http://[::1]:8080", want: "http://[::1]:8080"},
		{in: "https://127.0.0.1:8080", wantErr: "loopback http URL"},
		{in: "127.0.0.1:8080", wantErr: "loopback http URL"},
		{in: "http://user:pw@127.0.0.1:8080", wantErr: "credentials"}, //nolint:gosec // the URL with credentials is the case under test
		{in: "http://192.168.1.5:8080", wantErr: "only loopback hosts"},
		{in: "http://example.com", wantErr: "only loopback hosts"},
	}
	for _, tt := range tests {
		u, err := ParseLoopbackURL(tt.in)
		switch {
		case tt.wantErr != "":
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ParseLoopbackURL(%q) error = %v, want %q", tt.in, err, tt.wantErr)
			}
		case err != nil || u.String() != tt.want:
			t.Errorf("ParseLoopbackURL(%q) = %v, %v; want %q", tt.in, u, err, tt.want)
		default:
			// Accepted as expected.
		}
	}
}

// A rejected URL may carry a password; no error may repeat any part of it.
func TestParseLoopbackURLErrorsNeverEchoCredentials(t *testing.T) {
	for _, in := range []string{
		"http://user:hunter2@127.0.0.1:8080",
		"http://user:hunter2@example.com",
		"http://user:hunter2@[::1",
		"http://user:hunter2@127.0.0.1:notaport",
		"ftp://user:hunter2@127.0.0.1",
		"user:hunter2@127.0.0.1:8080",
		"http://example.com/?token=hunter2",
		"http://192.168.1.5/?token=hunter2",
	} {
		_, err := ParseLoopbackURL(in) //nolint:gosec // credentials in the URL are the case under test
		if err == nil {
			t.Errorf("ParseLoopbackURL(%q) accepted", in)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("ParseLoopbackURL(%q) error echoes the credential: %v", in, err)
		}
	}
}

func runtimeServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runtime" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeRuntime(t *testing.T) {
	srv := runtimeServer(t, `{"protocol_version":1,"mode":"core","run_id":"r1","ready":true,"instance_id":"abc"}`, 200)
	rt, err := ProbeRuntime(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if rt.RunID != "r1" || rt.InstanceID != "abc" || !rt.Ready || rt.Mode != "core" {
		t.Errorf("runtime = %+v", rt)
	}
}

func TestProbeRuntimeRejects(t *testing.T) {
	tests := map[string]struct {
		body   string
		status int
	}{
		"wrong protocol": {`{"protocol_version":2,"run_id":"r"}`, 200},
		"not json":       {`<html>`, 200},
		"server error":   {`{}`, 500},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			srv := runtimeServer(t, tt.body, tt.status)
			if _, err := ProbeRuntime(context.Background(), srv.URL); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestProbeRuntimeNeverFollowsRedirects(t *testing.T) {
	target := runtimeServer(t, `{"protocol_version":1,"run_id":"r1"}`, 200)
	redirector := httptest.NewServer(http.RedirectHandler(target.URL+"/api/runtime", http.StatusFound))
	t.Cleanup(redirector.Close)
	if _, err := ProbeRuntime(context.Background(), redirector.URL); err == nil {
		t.Error("a redirect must not be followed")
	}
}

func TestProbeRuntimeRefusesNonLoopback(t *testing.T) {
	if _, err := ProbeRuntime(context.Background(), "http://192.0.2.1:8080"); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Errorf("err = %v, want a loopback refusal", err)
	}
}
