package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	afmsdk "github.com/akopichin/afm/sdk"
)

var ErrStaleRun = errors.New("registration belongs to a different AFM run")
var ErrCompletedRun = errors.New("AFM run is complete")

type Registry struct{ Store *Store }

func NewRegistry(store *Store) *Registry { return &Registry{Store: store} }
func sameRun(b RunBinding, s afmsdk.RunStatus) bool {
	return b.StartedAt.Equal(s.StartedAt) && (b.OptionalAFMRunID == "" || s.RunID == "" || b.OptionalAFMRunID == s.RunID)
}
func terminal(s afmsdk.RunStatus) bool {
	return s.RunState == "finished" || s.RunState == "failed" || s.RunState == "interrupted" || s.EndedAt != nil
}
func (r *Registry) Register(ctx context.Context, port int) (RunBinding, error) {
	run, err := afmsdk.Connect(ctx, port)
	if err != nil {
		return RunBinding{}, err
	}
	status, err := run.Status(ctx)
	if err != nil {
		return RunBinding{}, err
	}
	if status.StartedAt.IsZero() {
		return RunBinding{}, errors.New("AFM started_at is missing")
	}
	now := time.Now().UTC()
	var result RunBinding
	err = r.Store.Update(func(st *State) error {
		for key, b := range st.Runs {
			if b.DashboardPort != port || b.Invalidated {
				continue
			}
			if sameRun(b, status) {
				b.LastSeenAt = now
				b.Offline = false
				b.Completed = terminal(status)
				st.Runs[key] = b
				result = b
				return nil
			}
			b.Invalidated = true
			st.Runs[key] = b
			for id, o := range st.Outbox {
				if o.RunKey == key && (o.Status == ReceiptQueued || o.Status == ReceiptUnknown) {
					markOutboundFailed(&o, "AFM run changed")
					st.Outbox[id] = o
				}
			}
		}
		id, err := randomID()
		if err != nil {
			return err
		}
		result = RunBinding{Key: RunKey(id), DashboardPort: port, StartedAt: status.StartedAt, OptionalAFMRunID: status.RunID, LastSeenAt: now, Label: status.FlowName, Completed: terminal(status)}
		st.Runs[result.Key] = result
		return nil
	})
	return result, err
}
func (r *Registry) Resolve(ctx context.Context, key RunKey, write bool) (*afmsdk.Run, afmsdk.RunStatus, error) {
	b, ok := r.Store.Run(key)
	if !ok || b.Invalidated {
		return nil, afmsdk.RunStatus{}, ErrStaleRun
	}
	run, err := afmsdk.Connect(ctx, b.DashboardPort)
	if err != nil {
		_ = r.Store.Update(func(st *State) error { x := st.Runs[key]; x.Offline = true; st.Runs[key] = x; return nil })
		return nil, afmsdk.RunStatus{}, err
	}
	status, err := run.Status(ctx)
	if err != nil {
		return nil, status, err
	}
	if !sameRun(b, status) {
		_ = r.Store.Update(func(st *State) error {
			x := st.Runs[key]
			x.Invalidated = true
			st.Runs[key] = x
			for id, o := range st.Outbox {
				if o.RunKey == key && (o.Status == ReceiptQueued || o.Status == ReceiptUnknown) {
					markOutboundFailed(&o, "AFM run changed")
					st.Outbox[id] = o
				}
			}
			return nil
		})
		return nil, status, ErrStaleRun
	}
	_ = r.Store.Update(func(st *State) error {
		x := st.Runs[key]
		x.LastSeenAt = time.Now().UTC()
		x.Offline = false
		x.Completed = terminal(status)
		st.Runs[key] = x
		return nil
	})
	if write && terminal(status) {
		return nil, status, ErrCompletedRun
	}
	return run, status, nil
}
func loopbackRemote(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func (r *Registry) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /v1/runs/register", func(w http.ResponseWriter, req *http.Request) {
		if !loopbackRemote(req.RemoteAddr) {
			http.Error(w, "loopback only", http.StatusForbidden)
			return
		}
		if origin := req.Header.Get("Origin"); origin != "" {
			http.Error(w, "browser origin not allowed", http.StatusForbidden)
			return
		}
		if mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
			http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
			return
		}
		var body struct {
			DashboardPort int `json:"dashboard_port"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if body.DashboardPort < 1 || body.DashboardPort > 65535 {
			http.Error(w, "invalid dashboard_port", http.StatusBadRequest)
			return
		}
		b, err := r.Register(req.Context(), body.DashboardPort)
		if err != nil {
			http.Error(w, "cannot connect AFM dashboard", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"run_key": b.Key, "label": b.Label, "dashboard_port": b.DashboardPort})
	})
	return mux
}
func parsePort(s string) (int, error) {
	var n int
	if strings.TrimSpace(s) == "" {
		return 0, fmt.Errorf("missing port")
	}
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil || n < 1 || n > 65535 || fmt.Sprint(n) != s {
		return 0, fmt.Errorf("invalid port")
	}
	return n, nil
}
