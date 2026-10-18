package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Agent Mesh Phase 3, T3.2: the afm agent CLI's HTTP client over /api/agent-mesh/*. Reads the 0600
// AFM_MESH_DESCRIPTOR file (written by pkg/executor's setupMeshDescriptor) — owns no state of its own, and
// never reads/writes any other file: the server is the single source of truth.

// agentMeshDescriptor mirrors executor.MeshDescriptor's JSON shape (duplicated here, not imported, since
// cmd/afm already depends on pkg/executor only for other reasons — this keeps the CLI's on-disk contract
// independent of that package's internal type).
type agentMeshDescriptor struct {
	URL           string `json:"url"`
	Token         string `json:"token"`
	ParticipantID string `json:"participant_id"`
}

// agentCLIError carries a machine-readable Code alongside a human Message — afm agent's one error shape,
// printed to stderr as JSON by runAgentCmd, never mixed with the stdout result object.
type agentCLIError struct {
	Code    string
	Message string
}

func (e *agentCLIError) Error() string { return e.Message }

func newAgentCLIError(code, format string, args ...any) *agentCLIError {
	return &agentCLIError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// agentMeshClient is the thin HTTP client every afm agent subcommand uses.
type agentMeshClient struct {
	baseURL       string
	token         string
	participantID string
	httpClient    *http.Client
}

// loadAgentMeshClient reads AFM_MESH_DESCRIPTOR. Its own error is always an *agentCLIError with code
// "messaging_unavailable" and the exact wording the plan requires, so every subcommand fails the same clear
// way when mesh was never wired into this invocation.
func loadAgentMeshClient() (*agentMeshClient, error) {
	path := os.Getenv("AFM_MESH_DESCRIPTOR")
	if path == "" {
		return nil, newAgentCLIError("messaging_unavailable", "messaging is not available for this run")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, newAgentCLIError("messaging_unavailable", "messaging is not available for this run: %v", err)
	}
	var d agentMeshDescriptor
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, newAgentCLIError("messaging_unavailable", "messaging is not available for this run: malformed descriptor")
	}
	if d.URL == "" || d.Token == "" || d.ParticipantID == "" {
		return nil, newAgentCLIError("messaging_unavailable", "messaging is not available for this run: incomplete descriptor")
	}
	return &agentMeshClient{baseURL: d.URL, token: d.Token, participantID: d.ParticipantID, httpClient: &http.Client{Timeout: 30 * time.Second}}, nil
}

// newAgentRequestID mints a fresh idempotency key for one CLI invocation — each invocation is a new action,
// not a retry of a previous one the user wants deduplicated.
func newAgentRequestID() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return "cli-" + hex.EncodeToString(buf)
}

// do issues one agent-mesh HTTP call. body (if non-nil) is marshaled as the JSON request body; out (if
// non-nil) receives the decoded JSON response. A non-2xx response becomes an *agentCLIError whose Code is
// the server's {"error":"<code>"} value.
func (c *agentMeshClient) do(method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return newAgentCLIError("internal", "encode request: %v", err)
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.baseURL+"/"+path, reqBody)
	if err != nil {
		return newAgentCLIError("internal", "build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return newAgentCLIError("unreachable", "mesh server unreachable: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return newAgentCLIError("internal", "read response: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &apiErr)
		code := apiErr.Error
		if code == "" {
			code = "http_" + fmt.Sprint(resp.StatusCode)
		}
		if apiErr.Message != "" {
			// The server's reason tells the agent what to fix (e.g. "unknown thread … — send without a thread").
			return newAgentCLIError(code, "%s %s -> HTTP %d (%s): %s", method, path, resp.StatusCode, code, apiErr.Message)
		}
		return newAgentCLIError(code, "mesh request failed: %s %s -> HTTP %d (%s)", method, path, resp.StatusCode, code)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return newAgentCLIError("internal", "decode response: %v", err)
	}
	return nil
}

// asAgentCLIError normalizes any error into an *agentCLIError so every subcommand's failure path has a
// Code, even one that originated below do() (e.g. loadAgentMeshClient).
func asAgentCLIError(err error) *agentCLIError {
	var ce *agentCLIError
	if errors.As(err, &ce) {
		return ce
	}
	return &agentCLIError{Code: "internal", Message: err.Error()}
}
