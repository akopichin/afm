package afmsdk

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

const supportedRuntimeProtocol = 1

// runtimeInfo mirrors the /api/runtime response.
type runtimeInfo struct {
	ProtocolVersion int    `json:"protocol_version"`
	Mode            string `json:"mode"`
	RunID           string `json:"run_id"`
	Ready           bool   `json:"ready"`
}

// fetchRuntime reads /api/runtime; found is false for an afm that predates the endpoint (404).
func (r *Run) fetchRuntime(ctx context.Context) (info runtimeInfo, found bool, err error) {
	err = r.getJSON(ctx, "/api/runtime", &info)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
		return runtimeInfo{}, false, nil
	}
	if err != nil {
		return runtimeInfo{}, false, err
	}
	return info, true, nil
}

// check rejects runtimes this SDK cannot drive; an empty mode is tolerated like a missing endpoint.
func (info runtimeInfo) check() error {
	switch Mode(info.Mode) {
	case "", ModeDashboard:
		return nil
	case ModeCore:
		if info.ProtocolVersion != supportedRuntimeProtocol {
			return fmt.Errorf("%w: core protocol version %d, want %d", ErrIncompatibleRuntime, info.ProtocolVersion, supportedRuntimeProtocol)
		}
		return nil
	}
	return fmt.Errorf("%w: unknown mode %q", ErrIncompatibleRuntime, info.Mode)
}

// detectMode records the runtime mode of an already running afm (Connect, Attach).
func (r *Run) detectMode(ctx context.Context) error {
	info, found, err := r.fetchRuntime(ctx)
	if err != nil {
		return fmt.Errorf("afmsdk: detect runtime: %w", err)
	}
	if !found {
		return nil
	}
	if err := info.check(); err != nil {
		return err
	}
	r.mode = Mode(info.Mode)
	return nil
}
