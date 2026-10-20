package afmsdk

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConnectStatusAndPlan(t *testing.T) {
	plan := strings.Repeat("plan line\n", 600)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			_, _ = fmt.Fprint(w, `{"flow_name":"demo","started_at":"2026-10-20T06:00:00Z","last_seq":9,"run_status":"running","capabilities":{"collaboration":true},"stages":[{"id":"design","name":"Design","status":"awaiting_approval","updated_at":"2026-10-20T06:01:00Z","show_plan":true}]}`)
		case "/api/stages/design/plan":
			_, _ = fmt.Fprint(w, plan)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	run, err := Connect(context.Background(), port)
	if err != nil {
		t.Fatal(err)
	}
	if run.Port() != port || run.PID() != 0 {
		t.Fatalf("connected handle: port=%d pid=%d", run.Port(), run.PID())
	}
	status, err := run.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.RunID != "" || status.StartedAt.IsZero() || status.LastSeq != 9 || !status.Capabilities.Collaboration || len(status.StageViews) != 1 || status.StageViews[0].UpdatedAt.IsZero() || status.Stages["design"] != StageAwaitingApproval {
		t.Fatalf("status: %+v", status)
	}
	got, err := run.Plan(context.Background(), "design")
	if err != nil || string(got) != plan {
		t.Fatalf("plan len=%d error=%v", len(got), err)
	}
}

func TestConnectRejectsInvalidPortAndNonAFM(t *testing.T) {
	for _, port := range []int{-1, 0, 65536} {
		if _, err := Connect(context.Background(), port); err == nil {
			t.Fatalf("port %d accepted", port)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"flow_name":"other","stages":[]}`)
	}))
	defer srv.Close()
	if _, err := Connect(context.Background(), srv.Listener.Addr().(*net.TCPAddr).Port); err == nil {
		t.Fatal("zero started_at accepted")
	}
}

func TestPlanOversizeAndHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/big/plan") {
			_, _ = fmt.Fprint(w, strings.Repeat("p", maxPlanBytes+1))
			return
		}
		http.Error(w, "missing plan", http.StatusNotFound)
	}))
	defer srv.Close()
	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	if _, err := run.Plan(context.Background(), "big"); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
	_, err := run.Plan(context.Background(), "missing")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound {
		t.Fatalf("HTTP error = %v", err)
	}
}

func TestConnectRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := Connect(ctx, srv.Listener.Addr().(*net.TCPAddr).Port); err == nil {
		t.Fatal("expected context timeout")
	}
}
