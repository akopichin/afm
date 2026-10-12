package browser

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newFakeChromeConn returns a chromeConn backed by in-memory OS pipes so the
// read/write loops run harmlessly without a real Chrome.
func newFakeChromeConn(t *testing.T) *chromeConn {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
	})
	return &chromeConn{toChrome: inW, fromChrome: outR}
}

func newTestManager(t *testing.T, onLaunch func()) *Manager {
	t.Helper()
	m, err := New(Options{Epoch: "ep1", ChromePath: "/fake/chrome", LaunchTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	m.launch = func(_ context.Context, _, dir string) (*chromeConn, error) {
		if onLaunch != nil {
			onLaunch()
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		return newFakeChromeConn(t), nil
	}
	return m
}

func dialAuthed(t *testing.T, m *Manager, epoch, stage string) *websocket.Conn {
	t.Helper()
	url := "ws://" + m.Addr() + "/session/" + epoch + "/" + stage
	h := http.Header{"Authorization": {"Bearer " + m.Token()}}
	ws, _, err := websocket.DefaultDialer.Dial(url, h)
	if err != nil {
		t.Fatalf("dial %s/%s: %v", epoch, stage, err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	// Wait until the server-side session has finished its (single) launch so
	// launch counting is deterministic.
	key := epoch + "/" + stage
	deadline := time.After(2 * time.Second)
	for {
		m.mu.Lock()
		s := m.sessions[key]
		m.mu.Unlock()
		if s != nil {
			select {
			case <-s.launched:
				return ws
			case <-deadline:
				t.Fatal("launch did not complete in time")
			case <-time.After(5 * time.Millisecond):
			}
		} else {
			select {
			case <-deadline:
				t.Fatal("session never created")
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
}

func profileDirOf(m *Manager, epoch, stage string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[epoch+"/"+stage]
	if s == nil {
		return ""
	}
	return s.profileDir
}

func TestManager_WSWithoutTokenIs401_NoChrome(t *testing.T) {
	launched := 0
	m := newTestManager(t, func() { launched++ })
	url := "ws://" + m.Addr() + "/session/ep1/build"
	_, resp, err := websocket.DefaultDialer.Dial(url, nil) // no Authorization
	if err == nil {
		t.Fatal("want handshake failure")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %v", resp)
	}
	if launched != 0 {
		t.Fatal("Chrome must not launch on unauthorized handshake")
	}
}

func TestManager_ConcurrentFirstConnect_SingleLaunch(t *testing.T) {
	var launches int32
	m := newTestManager(t, func() { atomic.AddInt32(&launches, 1) })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() { dialAuthed(t, m, "ep1", "build") })
	}
	wg.Wait()
	if atomic.LoadInt32(&launches) != 1 {
		t.Fatalf("want 1 lazy launch, got %d", launches)
	}
}

func TestManager_TwoStages_SeparateProfiles(t *testing.T) {
	m := newTestManager(t, nil)
	dialAuthed(t, m, "ep1", "build")
	dialAuthed(t, m, "ep1", "test")
	if profileDirOf(m, "ep1", "build") == profileDirOf(m, "ep1", "test") {
		t.Fatal("stages must not share a Chrome profile")
	}
}

func TestManager_SameStageReconnect_ReusesSession(t *testing.T) {
	var launches int32
	m := newTestManager(t, func() { atomic.AddInt32(&launches, 1) })
	dialAuthed(t, m, "ep1", "build")
	dialAuthed(t, m, "ep1", "build") // retry/resume: same path
	if atomic.LoadInt32(&launches) != 1 {
		t.Fatalf("reconnect to same stage must reuse Chrome, got %d launches", launches)
	}
}

func TestManager_ChromeLaunchDeadline(t *testing.T) {
	blockForever := func() { select {} }
	m := newTestManager(t, blockForever)
	m.launchTimeout = 50 * time.Millisecond
	_, err := m.firstConnect(context.Background(), "ep1", "build")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("launch must time out, got %v", err)
	}
}

func TestManager_ClientCancelReleasesConnect(t *testing.T) {
	blockForever := func() { select {} }
	m := newTestManager(t, blockForever)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := m.firstConnect(ctx, "ep1", "build"); err == nil {
		t.Fatal("client cancellation must abort the pending launch")
	}
}

func writeOwnedProfile(t *testing.T, parent, name string, pid int) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeOwnerMarker(dir, pid, "epX"); err != nil {
		t.Fatal(err)
	}
	return dir
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn short-lived process: %v", err)
	}
	return cmd.Process.Pid // exited → dead
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func TestManager_SweepsStaleOwnedProfiles(t *testing.T) {
	parent := t.TempDir()
	stale := writeOwnedProfile(t, parent, "sess-dead", deadPID(t)) // marker pid not alive
	live := writeOwnedProfile(t, parent, "sess-live", os.Getpid()) // marker pid alive
	sweepStaleProfiles(parent, processAlive)                       // called by New
	if dirExists(stale) {
		t.Fatal("stale owned profile must be removed")
	}
	if !dirExists(live) {
		t.Fatal("a live manager's profile must NOT be touched")
	}
}

func TestManager_SweepLeavesUnmarkedDirs(t *testing.T) {
	parent := t.TempDir()
	unmarked := filepath.Join(parent, "no-marker")
	if err := os.MkdirAll(unmarked, 0o700); err != nil {
		t.Fatal(err)
	}
	sweepStaleProfiles(parent, func(int) bool { return false })
	if !dirExists(unmarked) {
		t.Fatal("a dir without a readable owner marker must be left untouched")
	}
}

func TestManager_WSWrongTokenIs401_NoChrome(t *testing.T) {
	launched := 0
	m := newTestManager(t, func() { launched++ })
	url := "ws://" + m.Addr() + "/session/ep1/build"
	h := http.Header{"Authorization": {"Bearer wrong-token"}}
	_, resp, err := websocket.DefaultDialer.Dial(url, h)
	if err == nil {
		t.Fatal("want handshake failure")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %v", resp)
	}
	if launched != 0 {
		t.Fatal("a wrong token must not launch Chrome")
	}
}

func TestManager_CloseDuringLaunch(t *testing.T) {
	m, err := New(Options{Epoch: "ep1", ChromePath: "/fake/chrome", LaunchTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	// The launcher owns a real pipe pair so we can observe whether chromeConn.close
	// ran (closing an already-closed *os.File returns an error).
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	t.Cleanup(func() { inR.Close(); outW.Close() })

	m.launch = func(_ context.Context, _, dir string) (*chromeConn, error) {
		close(started)
		<-release // block so Close runs while the launch is in flight
		_ = os.MkdirAll(dir, 0o700)
		return &chromeConn{toChrome: inW, fromChrome: outR}, nil
	}

	go func() { _, _ = m.firstConnect(context.Background(), "ep1", "build") }()
	<-started // launch goroutine is running; session is in the map

	closed := make(chan struct{})
	go func() { _ = m.Close(); close(closed) }()

	// Let Close reach its wait on s.launched, then let the launch finish.
	time.Sleep(20 * time.Millisecond)
	close(release)

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}

	// The launched Chrome's pipes must have been closed by teardown; a second
	// Close on toChrome therefore errors. If it succeeds, the conn was orphaned.
	if err := inW.Close(); err == nil {
		t.Fatal("Chrome launched during Close was orphaned: its conn was never closed")
	}
}

func TestSession_DeliverAfterClientDoneIsSafe(t *testing.T) {
	s := &session{done: make(chan struct{})}
	c := &wsClient{send: make(chan []byte, 1), done: make(chan struct{})}
	c.send <- []byte("fill") // saturate the queue so the send case is not ready
	close(c.done)            // client torn down

	// Must not panic (c.send is never closed) and must not dereference the nil ws
	// via the slow-client fallback — the closed done branch wins.
	s.deliver(c, []byte("x"), true)
	s.deliver(c, []byte("y"), false)
}

func TestManager_LaunchErrorNotCached(t *testing.T) {
	var calls int32
	m := newTestManager(t, nil)
	m.launch = func(_ context.Context, _, dir string) (*chromeConn, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return nil, errors.New("transient launch failure")
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		return newFakeChromeConn(t), nil
	}
	if _, err := m.firstConnect(context.Background(), "ep1", "build"); err == nil {
		t.Fatal("first connect should surface the launch failure")
	}
	if _, err := m.firstConnect(context.Background(), "ep1", "build"); err != nil {
		t.Fatalf("second connect must relaunch after a transient failure (not cache it), got %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("want 2 launch attempts, got %d", calls)
	}
}
