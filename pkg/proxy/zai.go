package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ZAITransform fixes 529 errors from api.z.ai by converting non-streaming
// requests to streaming and parsing the SSE response back to JSON.
type ZAITransform struct{}

// Match reports true for upstream URLs containing "api.z.ai".
func (z ZAITransform) Match(upstreamURL string) bool {
	return strings.Contains(upstreamURL, "api.z.ai")
}

// ServeHTTP passes stream=true requests through unchanged; for stream=false/absent
// it converts the request to streaming, collects the SSE response, and returns JSON.
func (z ZAITransform) ServeHTTP(w http.ResponseWriter, r *http.Request, upstream string) {
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		http.Error(w, "read body", http.StatusBadGateway)
		return
	}

	var bj map[string]any
	if jsonErr := json.Unmarshal(body, &bj); jsonErr != nil || streamRequested(bj) {
		// Already streaming or non-JSON body: passthrough as-is.
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		passthroughTo(upstream).ServeHTTP(w, r)
		return
	}

	// Add stream=true and forward to upstream.
	bj["stream"] = true
	newBody, _ := json.Marshal(bj)

	upstreamURL := upstream + r.URL.RequestURI()
	//nolint:gosec // upstream is config-controlled; the request path comes from the local agent
	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, bytes.NewReader(newBody))
	if err != nil {
		http.Error(w, "build upstream request", http.StatusBadGateway)
		return
	}
	for k, vv := range r.Header {
		if strings.EqualFold(k, "content-length") {
			continue
		}
		upstreamReq.Header[k] = vv
	}
	upstreamReq.Header.Set("Content-Length", fmt.Sprintf("%d", len(newBody)))
	upstreamReq.ContentLength = int64(len(newBody))

	//nolint:gosec // upstream is config-controlled
	resp, err := http.DefaultClient.Do(upstreamReq)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	sseBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "read upstream response", http.StatusBadGateway)
		return
	}

	if resp.StatusCode != http.StatusOK {
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(sseBytes) //nolint:errcheck
		return
	}

	msg, apiErr := parseSSE(sseBytes)
	if apiErr != nil {
		writeSSEError(w, 529, apiErr)
		return
	}
	out, _ := json.Marshal(msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(out) //nolint:errcheck
}

// BuildTransforms returns the ordered list of transforms for the given upstream.
// zai: nil = auto-detect by host ("api.z.ai"), true = always, false = never.
func BuildTransforms(upstream string, zai *bool) []Transform {
	var useZAI bool
	switch {
	case zai == nil:
		useZAI = strings.Contains(upstream, "api.z.ai")
	case *zai:
		useZAI = true
	default:
		useZAI = false
	}
	if useZAI {
		return []Transform{ZAITransform{}}
	}
	return nil
}

// --- SSE parser ---

type sseMessage struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Model      string         `json:"model"`
	Content    []sseBlock     `json:"content"`
	StopReason *string        `json:"stop_reason"`
	StopSeq    any            `json:"stop_sequence"`
	Usage      map[string]any `json:"usage,omitempty"`
}

type sseBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
}

type sseAPIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type blockAccum struct {
	bType     string
	id        string
	name      string
	text      strings.Builder
	thinking  strings.Builder
	inputJSON strings.Builder
	signature string
}

func parseSSE(data []byte) (*sseMessage, *sseAPIError) {
	blocks := map[int]*blockAccum{}
	var msg sseMessage
	gotMessage := false
	var stopReason string
	maxIdx := -1

	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := line[6:]
		if raw == "[DONE]" {
			break
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			continue
		}

		switch ev["type"] {
		case "error":
			errObj, _ := ev["error"].(map[string]any)
			t := sseStr(errObj, "type")
			if t == "" {
				t = "overloaded_error"
			}
			return nil, &sseAPIError{Type: t, Message: sseStr(errObj, "message")}

		case "message_start":
			m, _ := ev["message"].(map[string]any)
			msg.ID = sseStr(m, "id")
			msg.Type = "message"
			msg.Role = "assistant"
			msg.Model = sseStr(m, "model")
			if u, ok := m["usage"].(map[string]any); ok {
				msg.Usage = u
			}
			gotMessage = true

		case "content_block_start":
			idx := sseInt(ev, "index")
			cb, _ := ev["content_block"].(map[string]any)
			blocks[idx] = &blockAccum{
				bType: sseStr(cb, "type"),
				id:    sseStr(cb, "id"),
				name:  sseStr(cb, "name"),
			}
			if idx > maxIdx {
				maxIdx = idx
			}

		case "content_block_delta":
			idx := sseInt(ev, "index")
			delta, _ := ev["delta"].(map[string]any)
			b := blocks[idx]
			if b == nil {
				break
			}
			switch sseStr(delta, "type") {
			case "text_delta":
				b.text.WriteString(sseStr(delta, "text"))
			case "thinking_delta":
				b.thinking.WriteString(sseStr(delta, "thinking"))
			case "input_json_delta":
				b.inputJSON.WriteString(sseStr(delta, "partial_json"))
			case "signature_delta":
				b.signature = sseStr(delta, "signature")
			default:
				// ignore unknown delta types
			}

		case "message_delta":
			d, _ := ev["delta"].(map[string]any)
			stopReason = sseStr(d, "stop_reason")
			if u, ok := ev["usage"].(map[string]any); ok {
				if msg.Usage == nil {
					msg.Usage = map[string]any{}
				}
				for k, v := range u {
					msg.Usage[k] = v
				}
			}
		default:
			// ignore unknown event types
		}
	}

	if !gotMessage {
		return nil, &sseAPIError{Type: "overloaded_error", Message: "empty SSE response"}
	}

	for i := 0; i <= maxIdx; i++ {
		b, ok := blocks[i]
		if !ok {
			continue
		}
		blk := sseBlock{Type: b.bType}
		switch b.bType {
		case "text":
			blk.Text = b.text.String()
		case "thinking":
			blk.Thinking = b.thinking.String()
			blk.Signature = b.signature
		case "tool_use":
			blk.ID = b.id
			blk.Name = b.name
			var inp any
			if s := b.inputJSON.String(); s != "" {
				_ = json.Unmarshal([]byte(s), &inp)
			}
			blk.Input = inp
		default:
			// ignore unknown block types
		}
		msg.Content = append(msg.Content, blk)
	}

	if stopReason != "" {
		msg.StopReason = &stopReason
	}
	return &msg, nil
}

func writeSSEError(w http.ResponseWriter, status int, apiErr *sseAPIError) {
	body, _ := json.Marshal(map[string]any{"type": "error", "error": apiErr})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(body) //nolint:errcheck
}

// streamRequested reports whether the parsed request body explicitly sets "stream": true.
func streamRequested(body map[string]any) bool {
	b, _ := body["stream"].(bool)
	return b
}

func sseStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func sseInt(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}
