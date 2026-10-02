// Package browser implements the managed Chrome DevTools browser capability:
// a loopback WebSocket manager that lazily launches a dedicated Chrome per stage
// (via --remote-debugging-pipe) and proxies WebSocket frames to the CDP pipe, so
// an afm stage agent can drive a real browser through the afm_chrome MCP.
package browser

import "fmt"

// TransportProtocolVersion is the host↔container transport-compatibility version.
// The host probes a pinned image via `afm browser protocol-version` and refuses /
// degrades on a mismatch rather than hanging. Bump ONLY on a breaking transport
// change (descriptor shape, WS path scheme, framing).
const TransportProtocolVersion = 1

// Descriptor is the versioned runtime descriptor that tells the native or
// in-container `afm browser mcp` how to reach the manager and the tested app.
// The token is a secret and must never be logged or placed in argv.
type Descriptor struct {
	Version  int    `json:"version"`
	Endpoint string `json:"endpoint"` // host:port of the WS listener (loopback native, host.docker.internal in Docker)
	Token    string `json:"token"`
	AppURL   string `json:"app_url"`
	Epoch    string `json:"epoch"`
}

// StageEndpoint derives the deterministic per-stage WebSocket URL. The host and
// the in-container launcher compute the same value, so no registration RPC is
// needed: the manager lazily creates the session on the first authorized connect.
func (d *Descriptor) StageEndpoint(stageID string) string {
	return fmt.Sprintf("ws://%s/session/%s/%s", d.Endpoint, d.Epoch, stageID)
}
