package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func guardedRequest(method, host, origin, body string) *httptest.ResponseRecorder {
	reached := false
	h := localGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if reached != (w.Code == http.StatusNoContent) {
		panic("guard status and handler call disagree")
	}
	return w
}

func TestLocalGuardMatrix(t *testing.T) {
	cases := []struct {
		name         string
		method, host string
		origin       string
		want         int
	}{
		{"ipv4 with port", "POST", "127.0.0.1:8080", "", 204},
		{"localhost with port", "GET", "localhost:8080", "", 204},
		{"ipv6 with port", "POST", "[::1]:8080", "", 204},
		{"ipv6 without port", "POST", "[::1]", "", 204},
		{"localhost without port", "DELETE", "localhost", "", 204},
		{"same origin", "POST", "localhost:8080", "http://localhost:8080", 204},
		{"same origin uppercase host", "POST", "LOCALHOST:8080", "http://localhost:8080", 204},
		{"foreign host", "POST", "evil.example", "", 403},
		{"foreign host with port", "GET", "evil.example:8080", "", 403},
		{"lan ip", "POST", "192.168.1.5:8080", "", 403},
		{"container service name", "POST", "afm:8080", "", 403},
		{"localhost lookalike", "POST", "localhost.evil.example:8080", "", 403},
		{"foreign origin", "POST", "localhost:8080", "https://evil.example", 403},
		{"foreign origin on GET", "GET", "localhost:8080", "https://evil.example", 403},
		{"origin with other loopback port", "POST", "localhost:8080", "http://localhost:9999", 403},
		{"origin null", "POST", "localhost:8080", "null", 403},
		{"origin other loopback name", "POST", "localhost:8080", "http://127.0.0.1:8080", 403},
		{"origin non-http scheme", "POST", "localhost:8080", "file://localhost:8080", 403},
		{"method put", "PUT", "localhost:8080", "", 405},
		{"method options", "OPTIONS", "localhost:8080", "", 405},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w := guardedRequest(tc.method, tc.host, tc.origin, ""); w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

// A container listens on all interfaces, so the guard must judge the Host header, not the bind address.
func TestLocalGuardIgnoresBindAddress(t *testing.T) {
	srv := newTestServer(t, Config{Port: 8080})
	if addr := srv.httpSrv.Addr; addr != ":8080" && addr != "127.0.0.1:8080" {
		t.Logf("listen address %q", addr)
	}
	if w := guardedRequest("POST", "10.0.0.7:8080", "", ""); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestLocalGuardLimitsBody(t *testing.T) {
	big := strings.Repeat("x", maxLocalBodyBytes+1)
	if w := guardedRequest("POST", "localhost:8080", "", big); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared oversize: status = %d, want 413", w.Code)
	}

	h := localGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, maxLocalBodyBytes+2)
		n, err := r.Body.Read(buf)
		for err == nil {
			var m int
			m, err = r.Body.Read(buf[n:])
			n += m
		}
		if n > maxLocalBodyBytes {
			t.Errorf("read %d bytes past the limit", n)
		}
	}))
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(big))
	req.ContentLength = -1 // chunked: only the reader can enforce the cap
	req.Host = "localhost:8080"
	h.ServeHTTP(httptest.NewRecorder(), req)
}
