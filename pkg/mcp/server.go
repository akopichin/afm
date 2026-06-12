package mcp

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	jAskUser     = "ask_user"
	jContent     = "content"
	jDescription = "description"
	jID          = "id"
	jJSONRPC     = "2.0"
	jKeyJSONRPC  = "jsonrpc"
	jName        = "name"
	jQuestion    = "question"
	jString      = "string"
	jText        = "text"
	jTools       = "tools"
	jType        = "type"
)

// Notifier is implemented by orchestrator.McpNotifier so the MCP server can
// publish ask_user/user_answered events without importing orchestrator.
type Notifier interface {
	PublishAskUser(stageID, phase, qID, question string, options []string, allowCustom bool)
	PublishUserAnswered(stageID, phase, qID, answer string)
	SetStageStatus(stageID string, awaitingInput bool, phase string)
}

// defaultPollingTimeout is the per-RPC wait before returning a "pending"
// success result. It is shorter than the Claude CLI MCP client's tool
// timeout (~60s) so the agent never sees the call as failed — it just
// retries with the same id, and the answer is replayed via FindEntry
// once it arrives.
const defaultPollingTimeout = 45 * time.Second

// Server is the MCP HTTP server. One instance handles all stages and phases;
// the URL /mcp/<stage>/<phase> distinguishes them.
type Server struct {
	runDir         string
	notifier       Notifier
	pollingTimeout time.Duration
	mu             sync.Mutex
	waiters        map[string]chan waiterEvent // key: stage|phase|qID
}

type waiterEvent struct {
	answer      string
	fromOptions bool
	cancelled   bool
}

func NewServer(runDir string, notifier Notifier) *Server {
	return &Server{
		runDir:         runDir,
		notifier:       notifier,
		pollingTimeout: defaultPollingTimeout,
		waiters:        make(map[string]chan waiterEvent),
	}
}

// SetPollingTimeout overrides the per-RPC wait. Intended for tests.
func (s *Server) SetPollingTimeout(d time.Duration) {
	s.pollingTimeout = d
}

// ServeHTTP routes /mcp/<stage>/<phase> to the JSON-RPC handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/mcp/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "expected /mcp/<stage>/<phase>", http.StatusBadRequest)
		return
	}
	stageID, phase := parts[0], parts[1]
	if !isSafeSegment(stageID) || !isSafeSegment(phase) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, nil, -32700, "parse error")
		return
	}

	switch req.Method {
	case "initialize":
		s.writeResult(w, req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{jTools: map[string]any{}},
			"serverInfo":      map[string]any{jName: "flowmanager", "version": "1"},
		})
	case "tools/list":
		s.writeResult(w, req.ID, map[string]any{
			jTools: []any{askUserToolSchema()},
		})
	case "tools/call":
		s.handleToolsCall(w, r, req.ID, req.Params, stageID, phase)
	default:
		s.writeError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

func askUserToolSchema() map[string]any {
	return map[string]any{
		jName:        jAskUser,
		jDescription: "Ask the human user a question and wait for their answer.",
		"inputSchema": map[string]any{
			jType:      "object",
			"required": []string{jID, jQuestion},
			"properties": map[string]any{
				jID:            map[string]any{jType: jString, jDescription: "Stable id for this question. Used for idempotent replay."},
				jQuestion:      map[string]any{jType: jString},
				"options":      map[string]any{jType: "array", "items": map[string]any{jType: jString}, jDescription: "Optional suggested answers."},
				"allow_custom": map[string]any{jType: "boolean", "default": true, jDescription: "Whether the user may type a freeform answer."},
			},
		},
	}
}

// toolsCallParams holds the parameters of a tools/call JSON-RPC request.
type toolsCallParams struct {
	Name      string             `json:"name"`
	Arguments toolsCallArguments `json:"arguments"`
}

type toolsCallArguments struct {
	ID          string   `json:"id"`
	Question    string   `json:"question"`
	Options     []string `json:"options"`
	AllowCustom *bool    `json:"allow_custom"`
}

func (s *Server) handleToolsCall(w http.ResponseWriter, r *http.Request, rpcID json.RawMessage, params json.RawMessage, stageID, phase string) {
	var p toolsCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		s.writeError(w, rpcID, -32602, "invalid params")
		return
	}
	if p.Name != jAskUser {
		s.writeError(w, rpcID, -32601, "unknown tool: "+p.Name)
		return
	}
	if p.Arguments.ID == "" || p.Arguments.Question == "" {
		s.writeError(w, rpcID, -32602, "id and question are required")
		return
	}
	allowCustom := true
	if p.Arguments.AllowCustom != nil {
		allowCustom = *p.Arguments.AllowCustom
	}

	dialogPath := filepath.Join(s.runDir, stageID, phase+".dialog.jsonl")

	existing, err := FindEntry(dialogPath, p.Arguments.ID)
	if err != nil {
		s.writeError(w, rpcID, -32603, "read dialog: "+err.Error())
		return
	}
	if existing != nil && existing.Answer != nil {
		s.writeToolResult(w, rpcID, *existing.Answer, existing.FromOptions)
		return
	}

	// Register waiter BEFORE appending question to avoid race:
	// if answer arrives between FindEntry and waiter registration,
	// we would miss it. After registration we re-check the file.
	key := waiterKey(stageID, phase, p.Arguments.ID)
	ch := make(chan waiterEvent, 1)
	s.mu.Lock()
	s.waiters[key] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiters, key)
		s.mu.Unlock()
	}()

	if existing == nil {
		if err := AppendQuestion(dialogPath, Question{
			ID: p.Arguments.ID, Question: p.Arguments.Question,
			Options: p.Arguments.Options, AllowCustom: allowCustom,
		}); err != nil {
			s.writeError(w, rpcID, -32603, "append question: "+err.Error())
			return
		}
		if s.notifier != nil {
			s.notifier.PublishAskUser(stageID, phase, p.Arguments.ID, p.Arguments.Question, p.Arguments.Options, allowCustom)
			s.notifier.SetStageStatus(stageID, true, phase)
		}
	}

	// Re-check: answer may have arrived after our first FindEntry
	// but before waiter was registered.
	if existing2, _ := FindEntry(dialogPath, p.Arguments.ID); existing2 != nil && existing2.Answer != nil {
		s.writeToolResult(w, rpcID, *existing2.Answer, existing2.FromOptions)
		return
	}

	// Non-blocking drain: if NotifyAnswer already sent to channel.
	select {
	case ev := <-ch:
		if ev.cancelled {
			s.writeToolErrorResult(w, rpcID, "cancelled by user")
		} else {
			s.writeToolResult(w, rpcID, ev.answer, ev.fromOptions)
		}
		return
	default:
	}

	// Block with a short polling timeout. If the user hasn't answered yet,
	// return a successful "pending" result so the Claude CLI MCP client
	// (which has its own ~60s tool timeout) does not see the call as failed.
	// The agent is instructed by the orchestrator's system prompt to retry
	// with the same id; FindEntry replays the real answer once it arrives.
	timeout := s.pollingTimeout
	if timeout <= 0 {
		timeout = defaultPollingTimeout
	}
	select {
	case ev := <-ch:
		if ev.cancelled {
			s.writeToolErrorResult(w, rpcID, "cancelled by user")
		} else {
			s.writeToolResult(w, rpcID, ev.answer, ev.fromOptions)
		}
	case <-time.After(timeout):
		s.writeToolPendingResult(w, rpcID, p.Arguments.ID)
	case <-r.Context().Done():
		return
	}
}

func waiterKey(stage, phase, qID string) string {
	return stage + "|" + phase + "|" + qID
}

// NotifyAnswer unblocks any waiting tools/call for the given question.
// If a waiter is active (the agent is still polling), the stage status is
// transitioned back from awaiting_user_input to planning/running so the UI
// reflects ongoing work. If no waiter is active (the agent has already
// stopped polling), status transition is left to the orchestrator's
// onUserAnswered handler, which will also restart the agent.
func (s *Server) NotifyAnswer(stageID, phase, qID, answer string, fromOptions bool) error {
	key := waiterKey(stageID, phase, qID)
	s.mu.Lock()
	ch, ok := s.waiters[key]
	s.mu.Unlock()
	delivered := false
	if ok {
		select {
		case ch <- waiterEvent{answer: answer, fromOptions: fromOptions}:
			delivered = true
		default:
		}
	}
	if s.notifier != nil {
		if delivered {
			s.notifier.SetStageStatus(stageID, false, phase)
		}
		s.notifier.PublishUserAnswered(stageID, phase, qID, answer)
	}
	return nil
}

// CancelStage cancels all pending waiters for the given stage.
func (s *Server) CancelStage(stageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := stageID + "|"
	for k, ch := range s.waiters {
		if strings.HasPrefix(k, prefix) {
			select {
			case ch <- waiterEvent{cancelled: true}:
			default:
			}
		}
	}
	return nil
}

func (s *Server) writeResult(w http.ResponseWriter, rpcID json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		jKeyJSONRPC: jJSONRPC,
		jID:         rawOrNull(rpcID),
		"result":    result,
	})
}

func (s *Server) writeError(w http.ResponseWriter, rpcID json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		jKeyJSONRPC: jJSONRPC,
		jID:         rawOrNull(rpcID),
		"error":     map[string]any{"code": code, "message": message},
	})
}

func (s *Server) writeToolResult(w http.ResponseWriter, rpcID json.RawMessage, answer string, fromOptions bool) {
	payload, _ := json.Marshal(map[string]any{"answer": answer, "from_options": fromOptions})
	s.writeResult(w, rpcID, map[string]any{
		jContent: []any{
			map[string]any{jType: jText, jText: string(payload)},
		},
	})
}

// writeToolPendingResult returns a success result that tells the agent the
// user has not answered yet and that it must call ask_user again with the
// same id. It is NOT an error — isError stays false — so the LLM does not
// treat the tool as broken and bail out of interactive mode.
func (s *Server) writeToolPendingResult(w http.ResponseWriter, rpcID json.RawMessage, qID string) {
	payload, _ := json.Marshal(map[string]any{
		"status":        "pending",
		jID:             qID,
		"retry_with_id": qID,
		"note":          "User has not answered yet. Call ask_user again with the SAME id to keep waiting. Do NOT proceed without the user's answer.",
	})
	s.writeResult(w, rpcID, map[string]any{
		jContent: []any{
			map[string]any{jType: jText, jText: string(payload)},
		},
	})
}

func (s *Server) writeToolErrorResult(w http.ResponseWriter, rpcID json.RawMessage, message string) {
	s.writeResult(w, rpcID, map[string]any{
		"isError": true,
		jContent: []any{
			map[string]any{jType: jText, jText: message},
		},
	})
}

func rawOrNull(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

// safeSegmentRe restricts URL segments to alphanumeric, dash, underscore.
var safeSegmentRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// isSafeSegment rejects path traversal and special characters in URL segments.
func isSafeSegment(s string) bool {
	return safeSegmentRe.MatchString(s)
}

// Compile-time check that *Server implements http.Handler.
var _ http.Handler = (*Server)(nil)
