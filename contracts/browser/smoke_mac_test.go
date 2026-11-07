//go:build browser_smoke

// Opt-in real-Chrome smoke test.
package browser

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func smokeChromePath(t *testing.T) string {
	t.Helper()
	candidates := []string{
		os.Getenv("AFM_BROWSER_CHROME"),
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	t.Skip("no Chrome found; set AFM_BROWSER_CHROME to run the smoke test")
	return ""
}

func TestSmoke_RealChromeCDPRoundTrip(t *testing.T) {
	chrome := smokeChromePath(t)
	epoch, err := NewEpoch()
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{ChromePath: chrome, Headless: true, Epoch: epoch, LaunchTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	url := "ws://" + m.Addr() + "/session/" + epoch + "/smoke"
	ws, _, err := websocket.DefaultDialer.Dial(url, map[string][]string{"Authorization": {"Bearer " + m.Token()}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// A real CDP request against a real Chrome: Browser.getVersion returns a result.
	if err := ws.WriteMessage(websocket.TextMessage, []byte(`{"id":1,"method":"Browser.getVersion"}`)); err != nil {
		t.Fatal(err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		_, data, rerr := ws.ReadMessage()
		if rerr != nil {
			t.Fatalf("read: %v", rerr)
		}
		var resp struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(data, &resp) != nil {
			continue
		}
		if resp.ID == 1 {
			if len(resp.Result) == 0 {
				t.Fatalf("Browser.getVersion returned no result: %s", data)
			}
			return // success
		}
		// otherwise it was an unsolicited event; keep reading
	}
}
