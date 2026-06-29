package proxy

import "net/http"

// Transform handles proxied HTTP requests for a specific upstream.
type Transform interface {
	// Match reports whether this transform applies to the given upstream URL.
	Match(upstreamURL string) bool
	// ServeHTTP handles the proxied request.
	// upstream is the validated upstream base URL (e.g. "https://api.z.ai/api/anthropic").
	ServeHTTP(w http.ResponseWriter, r *http.Request, upstream string)
}
