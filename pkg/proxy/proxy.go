package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// Proxy is a reverse proxy that applies the first matching Transform to each request.
// Requests with no matching transform are passed through unchanged.
type Proxy struct {
	upstream   string
	transforms []Transform
	srv        *http.Server
	addr       string
}

// New creates a Proxy forwarding to upstream with the given transforms.
func New(upstream string, transforms []Transform) *Proxy {
	return &Proxy{upstream: upstream, transforms: transforms}
}

// Start listens on 127.0.0.1:port (0 = OS-assigned free port) and returns
// the listen address as "http://127.0.0.1:PORT". The server runs in background.
func (p *Proxy) Start(port int) (string, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", fmt.Errorf("proxy listen: %w", err)
	}
	p.addr = "http://" + ln.Addr().String()
	p.srv = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go p.srv.Serve(ln) //nolint:errcheck
	return p.addr, nil
}

// Addr returns the proxy listen address (empty before Start).
func (p *Proxy) Addr() string { return p.addr }

// Shutdown gracefully stops the proxy server.
func (p *Proxy) Shutdown(ctx context.Context) error {
	if p.srv == nil {
		return nil
	}
	return p.srv.Shutdown(ctx)
}

// ServeHTTP dispatches to the first matching transform, or falls back to passthrough.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, t := range p.transforms {
		if t.Match(p.upstream) {
			t.ServeHTTP(w, r, p.upstream)
			return
		}
	}
	passthroughTo(p.upstream).ServeHTTP(w, r)
}

// passthroughTo returns a ReverseProxy that forwards to the given upstream base URL,
// prepending the upstream's path to the original request path.
func passthroughTo(upstream string) *httputil.ReverseProxy {
	target, _ := url.Parse(upstream)
	targetPath := target.Path
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			req.URL.Path = targetPath + req.URL.Path
		},
	}
}
