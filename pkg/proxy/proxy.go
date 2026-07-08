package proxy

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"
)

// Proxy is a reverse proxy that applies the first matching Transform to each request.
// Requests with no matching transform are passed through unchanged. When usageLogPath
// is non-empty, every proxied response's usage is captured to that file uniformly —
// regardless of whether a Transform handled the request or it passed through.
type Proxy struct {
	upstream     string
	transforms   []Transform
	usageLogPath string
	srv          *http.Server
	addr         string
}

// New creates a Proxy forwarding to upstream with the given transforms.
// usageLogPath is the file appended with one UsageRecord per proxied response; an
// empty string disables capture (a valid convention used in tests).
func New(upstream string, transforms []Transform, usageLogPath string) *Proxy {
	return &Proxy{
		upstream:     upstream,
		transforms:   transforms,
		usageLogPath: usageLogPath,
	}
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
// Both paths write through a tee-writer so usage can be captured uniformly afterward —
// this is the fix that makes passthrough responses counted too, not just Transform ones.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tw := &teeResponseWriter{w: w}
	p.dispatch(tw, r)
	p.captureUsage(tw, r)
}

// dispatch routes the request through the wrapped writer: the first matching transform
// handles it, otherwise a plain reverse proxy passes it through unchanged.
func (p *Proxy) dispatch(w http.ResponseWriter, r *http.Request) {
	for _, t := range p.transforms {
		if t.Match(p.upstream) {
			t.ServeHTTP(w, r, p.upstream)
			return
		}
	}
	passthroughTo(p.upstream).ServeHTTP(w, r)
}

// captureUsage runs the post-dispatch capture algorithm: parse the buffered response
// into a UsageRecord, fill the measured byte sizes, and append it to usageLogPath.
// Capture is a no-op when usageLogPath is empty, and any failure is logged and
// swallowed — the client-visible response is already sent, so capture must never
// alter or delay it.
func (p *Proxy) captureUsage(tw *teeResponseWriter, r *http.Request) {
	if p.usageLogPath == "" {
		return
	}
	// Non-200 responses (errors, rate limits, etc.) carry no usage field — skip silently.
	if tw.statusCode != 0 && tw.statusCode != http.StatusOK {
		return
	}
	contentType := tw.w.Header().Get("Content-Type")
	record, err := ParseUsage(contentType, tw.buf.String())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: proxy usage parse: %v\n", err)
		return
	}
	record.Timestamp = time.Now()
	if r.ContentLength > 0 {
		record.RequestBytes = int(r.ContentLength)
	}
	record.ResponseBytes = tw.buf.Len()
	if err := AppendUsageRecord(p.usageLogPath, record); err != nil {
		fmt.Fprintf(os.Stderr, "warning: proxy usage append: %v\n", err)
	}
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

// teeResponseWriter forwards every call to the underlying http.ResponseWriter while
// also buffering the written body bytes for post-dispatch usage capture. Each request
// gets its own instance (a per-ServeHTTP local), so there is no shared buffer across
// concurrent requests. Forwards are immediate on every Write, so client-visible
// streaming latency is unaffected — capture only happens after the handler returns.
type teeResponseWriter struct {
	w          http.ResponseWriter
	buf        bytes.Buffer
	statusCode int
}

func (tw *teeResponseWriter) Header() http.Header { return tw.w.Header() }

func (tw *teeResponseWriter) Write(b []byte) (int, error) {
	tw.buf.Write(b) //nolint:errcheck // bytes.Buffer.Write is documented as never failing
	return tw.w.Write(b)
}

func (tw *teeResponseWriter) WriteHeader(code int) {
	tw.statusCode = code
	tw.w.WriteHeader(code)
}
