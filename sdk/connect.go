package afmsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxJSONBytes  = 8 << 20
	maxPlanBytes  = 16 << 20
	maxErrorBytes = 4 << 10
)

// ErrResponseTooLarge means the dashboard returned more bytes than this SDK accepts.
// No partial response is returned to the caller.
var ErrResponseTooLarge = errors.New("afmsdk: response too large")

// HTTPError exposes the status of a rejected dashboard request.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("afmsdk: dashboard HTTP %d: %s", e.StatusCode, e.Body)
}

// Connect opens an already running dashboard on the given local TCP port. It
// neither checks a PID nor starts or owns an afm process.
func Connect(ctx context.Context, dashboardPort int) (*Run, error) {
	if dashboardPort < 1 || dashboardPort > 65535 {
		return nil, fmt.Errorf("afmsdk: invalid dashboard port %d", dashboardPort)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(dashboardPort))
	transport := &http.Transport{
		DisableKeepAlives: true,
		Proxy:             nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
		},
	}
	run := &Run{
		baseURL: "http://" + addr,
		httpClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		port: dashboardPort,
	}
	status, err := run.Status(ctx)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("afmsdk: connect: %w", err)
	}
	if status.StartedAt.IsZero() || status.FlowName == "" {
		transport.CloseIdleConnections()
		return nil, errors.New("afmsdk: connect: dashboard status lacks flow_name or started_at")
	}
	return run, nil
}

func (r *Run) request(ctx context.Context, method, path string, body any, limit int64) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("afmsdk: encode %s: %w", path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("afmsdk: %s: %w", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPost {
		req.Header.Set("Origin", r.baseURL)
	}
	client := r.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("afmsdk: %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("afmsdk: read %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("afmsdk: %s exceeds %d bytes: %w", path, limit, ErrResponseTooLarge)
	}
	return data, nil
}

func (r *Run) getJSON(ctx context.Context, path string, out any) error {
	data, err := r.request(ctx, http.MethodGet, path, nil, maxJSONBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("afmsdk: decode %s: %w", path, err)
	}
	return nil
}

func (r *Run) postJSON(ctx context.Context, path string, body any, out any) error {
	data, err := r.request(ctx, http.MethodPost, path, body, maxJSONBytes)
	if err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("afmsdk: decode %s: %w", path, err)
		}
	}
	return nil
}

func stagePath(stageID, suffix string) string {
	return "/api/stages/" + url.PathEscape(stageID) + suffix
}

// Plan returns the complete plan bytes used for rendering and digest checks.
func (r *Run) Plan(ctx context.Context, stageID string) ([]byte, error) {
	return r.request(ctx, http.MethodGet, stagePath(stageID, "/plan"), nil, maxPlanBytes)
}
