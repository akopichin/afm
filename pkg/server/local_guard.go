package server

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// maxLocalBodyBytes caps a guarded request body; the guarded routes carry small JSON-RPC and command bodies.
const maxLocalBodyBytes = 1 << 20

// LocalGuard admits only requests a local client could make: loopback Host, absent or same-origin Origin, and
// the methods guarded routes use. A container binds all interfaces, so the bind address proves nothing.
func LocalGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodPost, http.MethodDelete:
		default:
			w.Header().Set("Allow", "GET, POST, DELETE")
			writeFlowError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if !isLoopbackHost(r.Host) {
			writeFlowError(w, http.StatusForbidden, "forbidden_host")
			return
		}
		if !originAllowed(r.Header.Get("Origin"), r.Host) {
			writeFlowError(w, http.StatusForbidden, "forbidden_origin")
			return
		}
		if r.ContentLength > maxLocalBodyBytes {
			writeFlowError(w, http.StatusRequestEntityTooLarge, "body_too_large")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxLocalBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// isLoopbackHost accepts 127.0.0.1, localhost and [::1], with or without a port.
func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// originAllowed accepts a missing Origin (non-browser clients) or an http(s) Origin naming exactly the
// request's own loopback Host.
func originAllowed(origin, host string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !isLoopbackHost(u.Host) {
		return false
	}
	return strings.EqualFold(u.Host, host)
}
