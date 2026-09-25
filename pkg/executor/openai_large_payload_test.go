package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Real curl exercises serialization and exec limits, including on Linux CI.
// Fake curl tests above still cover protocol details and error accounting.
func runAdapterHTTP(t *testing.T, script, prompt, dir string, responses ...string) ([][]byte, string) {
	t.Helper()
	for _, name := range []string{"bash", "jq", "curl"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s not available", name)
		}
	}
	requests := make(chan []byte, len(responses)+1)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		select {
		case requests <- body:
		default:
			t.Error("unexpected extra request")
		}
		idx := int(calls.Add(1)) - 1
		if idx >= len(responses) {
			http.Error(w, "unexpected extra turn", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, responses[idx])
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	tmp := t.TempDir()
	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "OPENAI_API_KEY=test", "OPENAI_BASE_URL="+server.URL,
		"OPENAI_MODEL=test", "OPENAI_AGENT_MAX_TURNS=5", "TMPDIR="+tmp,
		"NO_PROXY=127.0.0.1", "no_proxy=127.0.0.1")
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("adapter failed: %v; stderr: %s", err, stderr.String())
	}
	if int(calls.Load()) != len(responses) {
		t.Fatalf("got %d requests, want %d; stderr: %s", calls.Load(), len(responses), stderr.String())
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("adapter left temporary files: %v, err=%v", entries, err)
	}
	var bodies [][]byte
	for range responses {
		bodies = append(bodies, <-requests)
	}
	return bodies, stdout.String()
}

func adapterSSE(t *testing.T, delta map[string]any, finish string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"choices": []any{map[string]any{
		"index": 0, "delta": delta, "finish_reason": finish,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(b) + "\ndata: [DONE]\n"
}

func adapterToolSSE(t *testing.T, command, content string) string {
	t.Helper()
	args, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return adapterSSE(t, map[string]any{"content": content, "tool_calls": []any{map[string]any{
		"index": 0, "id": "call_large", "type": "function",
		"function": map[string]string{"name": "bash", "arguments": string(args)},
	}}}, "tool_calls")
}

type adapterTestMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
}

func adapterMessages(t *testing.T, body []byte) []adapterTestMessage {
	t.Helper()
	var request struct {
		Messages []adapterTestMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	return request.Messages
}

func requireAdapterText(t *testing.T, raw json.RawMessage, want string) {
	t.Helper()
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("text changed in transit: got %d bytes, want %d", len(got), len(want))
	}
}

func requireAdapterFinal(t *testing.T, output, want string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 || !strings.Contains(lines[len(lines)-1], `"subtype":"success"`) {
		t.Fatal("missing success result")
	}
	ev, ok := parseStreamEvent(lines[len(lines)-2])
	if !ok || len(ev.Message.Content) != 1 || ev.Message.Content[0].Text != want {
		t.Fatal("final answer changed in transit")
	}
}

// A valid, incompressible PNG makes each data URL itself exceed 128 KiB.
func largeAdapterPNG(t *testing.T) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	if _, err := rand.Read(img.Pix); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "large image.png")
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func requireAdapterImages(t *testing.T, raw json.RawMessage, text string, paths ...string) {
	t.Helper()
	var blocks []struct {
		Type     string            `json:"type"`
		Text     string            `json:"text"`
		ImageURL map[string]string `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != len(paths)+1 || blocks[0].Type != "text" || blocks[0].Text != text {
		t.Fatal("multimodal content/text changed in transit")
	}
	for i, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
		if blocks[i+1].Type != "image_url" || blocks[i+1].ImageURL["url"] != want {
			t.Fatalf("image %d changed in transit", i)
		}
	}
}

func TestOpenAIAdapters_LargePromptAndResponse(t *testing.T) {
	for name, script := range map[string]string{"text": scriptPath(t), "agent": agentScriptPath(t)} {
		t.Run(name, func(t *testing.T) {
			// Includes multibyte characters and JSON/shell metacharacters.
			prompt := strings.Repeat("данные \"quotes\" \\ tab\t $(echo literal)\n", 6000) + "END"
			answer := strings.Repeat("ответ \"quoted\" \\ \n", 10000) + "DONE"
			bodies, out := runAdapterHTTP(t, script, prompt, t.TempDir(), adapterSSE(t, map[string]any{"content": answer}, "stop"))
			msgs := adapterMessages(t, bodies[0])
			requireAdapterText(t, msgs[len(msgs)-1].Content, prompt)
			requireAdapterFinal(t, out, answer)
		})
	}
}

func TestOpenAIAdapters_LargeImages(t *testing.T) {
	for name, script := range map[string]string{"text": scriptPath(t), "agent": agentScriptPath(t)} {
		t.Run(name, func(t *testing.T) {
			first, second := largeAdapterPNG(t), largeAdapterPNG(t)
			text := strings.Repeat("image context ", 12000)
			prompt := fmt.Sprintf("%s[Screenshot: %s] and [Screenshot: %s]", text, first, second)
			bodies, out := runAdapterHTTP(t, script, prompt, t.TempDir(), adapterSSE(t, map[string]any{"content": "ok"}, "stop"))
			msgs := adapterMessages(t, bodies[0])
			requireAdapterImages(t, msgs[len(msgs)-1].Content, text+" and ", first, second)
			requireAdapterFinal(t, out, "ok")
		})
	}
}

func TestOpenAIAgent_LargeHistory(t *testing.T) {
	prompt := strings.Repeat("x", 118000)
	bodies, out := runAdapterHTTP(t, agentScriptPath(t), prompt, t.TempDir(),
		adapterToolSSE(t, "printf '%015000d' 0", ""),
		adapterSSE(t, map[string]any{"content": "ok"}, "stop"))
	if len(bodies[0]) >= 128*1024 || len(bodies[1]) < 128*1024 {
		t.Fatalf("expected history to cross 128 KiB: request sizes %d, %d", len(bodies[0]), len(bodies[1]))
	}
	msgs := adapterMessages(t, bodies[1])
	if len(msgs) != 4 || msgs[3].Role != "tool" || msgs[3].ToolCallID != "call_large" {
		t.Fatal("missing linked tool result")
	}
	requireAdapterText(t, msgs[1].Content, prompt)
	requireAdapterText(t, msgs[3].Content, strings.Repeat("0", 15000)+"\n[exit code: 0]")
	requireAdapterFinal(t, out, "ok")
}

func TestOpenAIAgent_LargeCommandAndToolImage(t *testing.T) {
	dir := t.TempDir()
	pngPath := largeAdapterPNG(t)
	text := strings.Repeat("данные \"quotes\" \\ $(echo literal)\n", 7000)
	command := "cat > generated.txt <<'AFM_EOF'\n" + text + "AFM_EOF\n" +
		fmt.Sprintf("printf '%%s\\n' '[Screenshot: %s]'\nexit 7", pngPath)
	commentary := strings.Repeat("large assistant message\n", 7000)
	bodies, out := runAdapterHTTP(t, agentScriptPath(t), "create file", dir,
		adapterToolSSE(t, command, commentary), adapterSSE(t, map[string]any{"content": "ok"}, "stop"))
	tool, detail, ok := ParseToolAction(strings.SplitN(out, "\n", 2)[0], 0)
	if !ok || tool != "Bash" || detail != command {
		idx := 0
		for idx < len(detail) && idx < len(command) && detail[idx] == command[idx] {
			idx++
		}
		t.Fatalf("large command changed: tool=%s ok=%v bytes=%d want=%d difference=%d got=%q want=%q", tool, ok, len(detail), len(command), idx, detail[max(0, idx-40):min(len(detail), idx+80)], command[max(0, idx-40):min(len(command), idx+80)])
	}
	data, err := os.ReadFile(filepath.Join(dir, "generated.txt"))
	if err != nil || string(data) != text {
		idx := 0
		for idx < len(data) && idx < len(text) && data[idx] == text[idx] {
			idx++
		}
		t.Fatalf("large command did not write exact file: bytes=%d, err=%v, first difference=%d, suffix=%q", len(data), err, idx, data[max(0, len(data)-100):])
	}
	msgs := adapterMessages(t, bodies[1])
	if len(msgs) != 5 || msgs[2].Role != "assistant" || msgs[3].Role != "tool" || msgs[4].Role != "user" {
		t.Fatal("missing assistant/tool/image history")
	}
	requireAdapterText(t, msgs[2].Content, commentary)
	requireAdapterText(t, msgs[3].Content, fmt.Sprintf("[Screenshot: %s]\n[exit code: 7]", pngPath))
	requireAdapterImages(t, msgs[4].Content, "Screenshot referenced in the tool result above:", pngPath)
	requireAdapterFinal(t, out, "ok")
}
