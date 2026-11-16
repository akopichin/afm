package mcpserver

import (
	"encoding/json"
	"errors"
	"net/http"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/pkg/server"
)

// Codes that exist only in MCP; engine failures keep the code REST reports for them.
const (
	codeRunChanged = "run_changed"
	codeNotAllowed = "not_allowed"
	codeClosed     = "mcp_closed"
)

// codeInternal is the fallback for a failure no classifier names, the same code REST uses.
const codeInternal = "internal"

// Codes shared with REST; also reused as the Kind of failures raised inside MCP.
const (
	codeInvalid  = "invalid"
	codeConflict = "conflict"
)

// ToolError is the structured body of a failed tool call; clients branch on Code.
type ToolError struct {
	Code    string `json:"code"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	// State is the run's position after a control call that failed, so the client can decide again without a
	// separate read.
	State *postState `json:"state,omitempty"`
}

func (e *ToolError) Error() string { return e.Code + ": " + e.Message }

var errClosed = &ToolError{Code: codeClosed, Kind: "closed", Message: "the MCP service is shutting down"}

// toolError turns any failure into a ToolError. Engine errors go through the REST classifier, so both
// transports name the same failure the same way.
func toolError(err error) *ToolError {
	var te *ToolError
	if errors.As(err, &te) {
		return te
	}
	info := server.ClassifyEngineError(err)
	code := info.Code
	if code == "" {
		code = defaultCode(info.Status)
	}
	return &ToolError{Code: code, Kind: info.Kind, Message: info.Message}
}

func defaultCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return codeInvalid
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return codeConflict
	case http.StatusServiceUnavailable:
		return codeClosed
	}
	return codeInternal
}

// errorResult renders a ToolError as an isError tool result carrying the structured body.
func errorResult(err error) *sdkmcp.CallToolResult {
	te := toolError(err)
	res := &sdkmcp.CallToolResult{
		IsError:           true,
		StructuredContent: te,
		Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: te.Error()}},
	}
	return res
}

// okResult renders v as both structured content and a JSON text block for clients that read only text.
func okResult(v any) *sdkmcp.CallToolResult {
	raw, err := json.Marshal(v)
	if err != nil {
		return errorResult(err)
	}
	return &sdkmcp.CallToolResult{
		StructuredContent: json.RawMessage(raw),
		Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: string(raw)}},
	}
}
