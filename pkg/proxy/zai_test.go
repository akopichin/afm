package proxy_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/proxy"
)

func TestZAITransform_Match(t *testing.T) {
	z := proxy.ZAITransform{}
	if !z.Match("https://api.z.ai/api/anthropic") {
		t.Error("should match api.z.ai")
	}
	if z.Match("https://api.anthropic.com") {
		t.Error("should not match api.anthropic.com")
	}
	if z.Match("https://openai.com") {
		t.Error("should not match openai.com")
	}
}

func TestBuildTransforms_AutoDetect(t *testing.T) {
	ts := proxy.BuildTransforms("https://api.z.ai/api/anthropic", nil)
	if len(ts) == 0 {
		t.Error("auto-detect should enable ZAI for api.z.ai")
	}
	ts2 := proxy.BuildTransforms("https://api.anthropic.com", nil)
	if len(ts2) != 0 {
		t.Error("auto-detect should not enable ZAI for api.anthropic.com")
	}
}

func TestBuildTransforms_Override(t *testing.T) {
	tr := true
	ts := proxy.BuildTransforms("https://api.anthropic.com", &tr)
	if len(ts) == 0 {
		t.Error("explicit true should enable ZAI regardless of host")
	}
	f := false
	ts2 := proxy.BuildTransforms("https://api.z.ai/api/anthropic", &f)
	if len(ts2) != 0 {
		t.Error("explicit false should disable ZAI even for api.z.ai")
	}
}

func TestZAITransform_PassthroughStreaming(t *testing.T) {
	// stream=true → passthrough без изменений
	var receivedBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer upstream.Close()

	reqBody := `{"model":"glm-5.1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	proxy.ZAITransform{}.ServeHTTP(w, req, upstream.URL)

	var got map[string]any
	if err := json.Unmarshal(receivedBody, &got); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	if stream, _ := got["stream"].(bool); !stream {
		t.Errorf("upstream should receive stream=true, got body: %s", receivedBody)
	}
}

func TestZAITransform_ConvertNonStreaming(t *testing.T) {
	// stream absent → конвертируем в streaming, парсим SSE, возвращаем JSON
	sseBody := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg1","model":"glm-5.1","usage":{"input_tokens":5,"output_tokens":0}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello!"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	var gotStream bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var bj map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &bj) // body is test-controlled
		gotStream, _ = bj["stream"].(bool)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseBody))
	}))
	defer upstream.Close()

	reqBody := `{"model":"glm-5.1","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	proxy.ZAITransform{}.ServeHTTP(w, req, upstream.URL)

	if !gotStream {
		t.Error("upstream should have received stream=true")
	}
	if w.Code != 200 {
		t.Fatalf("status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v, body: %s", err, w.Body.String())
	}
	if resp["type"] != "message" {
		t.Errorf("type: got %v, want message", resp["type"])
	}
	content, _ := resp["content"].([]any)
	if len(content) == 0 {
		t.Fatal("content is empty")
	}
	block, _ := content[0].(map[string]any)
	if block["text"] != "Hello!" {
		t.Errorf("text: got %v, want Hello!", block["text"])
	}
}

func TestZAITransform_SSEError_Returns529(t *testing.T) {
	sseBody := "event: error\n" +
		`data: {"type":"error","error":{"type":"overloaded_error","message":"server overloaded"}}` + "\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseBody))
	}))
	defer upstream.Close()

	reqBody := `{"model":"glm-5.1","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))

	w := httptest.NewRecorder()
	proxy.ZAITransform{}.ServeHTTP(w, req, upstream.URL)

	if w.Code != 529 {
		t.Errorf("status: got %d, want 529", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v, body: %s", err, w.Body.String())
	}
	errObj, _ := resp["error"].(map[string]any)
	if errObj["type"] != "overloaded_error" {
		t.Errorf("error type: got %v", errObj["type"])
	}
}

func TestZAITransform_EmptySSE_Returns529(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// пустой body
	}))
	defer upstream.Close()

	reqBody := `{"model":"glm-5.1","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))

	w := httptest.NewRecorder()
	proxy.ZAITransform{}.ServeHTTP(w, req, upstream.URL)

	if w.Code != 529 {
		t.Errorf("status: got %d, want 529", w.Code)
	}
}

func TestZAITransform_UpstreamNon200_ForwardedAsIs(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte(`{"error":"service unavailable"}`))
	}))
	defer upstream.Close()

	reqBody := `{"model":"glm-5.1","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))

	w := httptest.NewRecorder()
	proxy.ZAITransform{}.ServeHTTP(w, req, upstream.URL)

	if w.Code != 503 {
		t.Errorf("status: got %d, want 503", w.Code)
	}
}
