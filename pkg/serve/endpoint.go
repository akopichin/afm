package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const (
	runtimeProtocol     = 1
	probeTimeout        = 3 * time.Second
	maxRuntimeBodyBytes = 64 << 10
	probeOp             = "runtime probe"
)

// ParseLoopbackURL accepts only a loopback http URL: an endpoint has no authentication, so nothing the host
// connects to or forwards may leave the machine.
func ParseLoopbackURL(raw string) (*url.URL, error) {
	// Errors never carry the URL: it may hold credentials, and url.Parse echoes its input in its own errors.
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return nil, errors.New("want a loopback http URL such as http://127.0.0.1:8080")
	}
	if u.User != nil {
		return nil, errors.New("URL credentials are not allowed")
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("only loopback hosts (127.0.0.1, localhost, [::1]) are allowed, not %q", host)
	}
	return u, nil
}

// Runtime is what an afm process says about itself on /api/runtime.
type Runtime struct {
	ProtocolVersion int    `json:"protocol_version"`
	Mode            string `json:"mode"`
	RunID           string `json:"run_id"`
	Ready           bool   `json:"ready"`
	// InstanceID is empty for an afm that predates it.
	InstanceID string `json:"instance_id"`
}

// noRedirectClient never follows a redirect: it would carry the request off the validated loopback endpoint.
var noRedirectClient = &http.Client{
	Timeout:       probeTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// ProbeRuntime asks the loopback endpoint who it is. Transport failures come back as *RemoteError.
func ProbeRuntime(ctx context.Context, endpoint string) (Runtime, error) {
	u, err := ParseLoopbackURL(endpoint)
	if err != nil {
		return Runtime{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.Scheme+"://"+u.Host+"/api/runtime", nil)
	if err != nil {
		return Runtime{}, remoteFailure(probeOp, err)
	}
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		return Runtime{}, remoteFailure(probeOp, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Runtime{}, fmt.Errorf("%s: HTTP %d", probeOp, resp.StatusCode)
	}
	var rt Runtime
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRuntimeBodyBytes)).Decode(&rt); err != nil {
		return Runtime{}, remoteFailure(probeOp, err)
	}
	if rt.ProtocolVersion != runtimeProtocol {
		return Runtime{}, fmt.Errorf("%s: protocol version %d, want %d", probeOp, rt.ProtocolVersion, runtimeProtocol)
	}
	return rt, nil
}
