// Package browser manages per-stage Chrome instances and proxies CDP through an authenticated WebSocket.
package browser

import "fmt"

// BrowserMode selects how the MCP package and the tested-app URL are produced.
type BrowserMode int

const (
	// BrowserModeNative runs afm on the host: the MCP is the already-installed
	// pinned chrome-devtools-mcp and the app is served on the host port directly.
	BrowserModeNative BrowserMode = iota
	// BrowserModeDocker runs the agent in the afm container: the MCP comes from the
	// image and the app URL is a host loopback publish.
	BrowserModeDocker
)

// TransportProtocolVersion is the host↔container transport-compatibility version. Bump ONLY on a breaking
// transport change (descriptor shape, WS path scheme, framing).
const TransportProtocolVersion = 1

// Descriptor is the versioned runtime descriptor that tells the native or in-container `afm browser mcp`
// how to reach the manager and the tested app.
type Descriptor struct {
	Version  int    `json:"version"`
	Endpoint string `json:"endpoint"` // host:port of the WS listener (loopback native, host.docker.internal in Docker)
	Token    string `json:"token"`
	AppURL   string `json:"app_url"`
	Epoch    string `json:"epoch"`
	// Docker reports whether the agent runs inside the afm container.
	Docker bool `json:"docker"`
}

// StageEndpoint derives the deterministic per-stage WebSocket URL.
func (d *Descriptor) StageEndpoint(stageID string) string {
	// Loopback / host.docker.internal transport authenticated by a bearer token;
	// plain ws is intentional (no TLS on localhost), so wss is not applicable.
	return fmt.Sprintf("ws://%s/session/%s/%s", d.Endpoint, d.Epoch, stageID) //nolint:revive // loopback transport, token-authenticated, no TLS
}
