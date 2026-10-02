package browser

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// defaultLaunchTimeout bounds a lazy Chrome launch + CDP handshake.
const defaultLaunchTimeout = 30 * time.Second

// writeQueueSize bounds the per-session serialized write queue to Chrome. A full
// queue applies backpressure and drops the slow session rather than growing.
const writeQueueSize = 256

// Options configures a Manager.
type Options struct {
	ChromePath    string        // Chrome executable to launch
	Headless      bool          // launch Chrome headless
	Epoch         string        // host-generated random session epoch ("b-"+hex)
	LaunchTimeout time.Duration // 0 → defaultLaunchTimeout
}

// Manager owns a loopback WebSocket listener, lazily launches a dedicated Chrome
// per stage session (its own profile), and proxies WebSocket frames ⇄ CDP pipe.
type Manager struct {
	epoch         string
	token         string
	chromePath    string
	headless      bool
	launchTimeout time.Duration

	ln   net.Listener
	srv  *http.Server
	root string // owned profiles parent

	// launch is the Chrome launcher (injectable for tests).
	launch  func(ctx context.Context, sessionKey, profileDir string) (*chromeConn, error)
	isAlive func(int) bool

	mu       sync.Mutex
	sessions map[string]*session
	closed   bool
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Loopback-only, token-authenticated; no cross-origin browser concern.
	CheckOrigin: func(*http.Request) bool { return true },
}

// New creates a loopback listener, an owned profiles parent, sweeps stale profiles
// from prior crashed runs, and starts serving. It does NOT launch Chrome.
func New(opts Options) (*Manager, error) {
	if opts.Epoch == "" {
		return nil, fmt.Errorf("browser: empty epoch")
	}
	tok, err := randomToken()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("browser: listen: %w", err)
	}
	root := filepath.Join(os.TempDir(), "afm-browser")
	if err := os.MkdirAll(root, 0o700); err != nil {
		ln.Close()
		return nil, fmt.Errorf("browser: profiles root: %w", err)
	}

	lt := opts.LaunchTimeout
	if lt == 0 {
		lt = defaultLaunchTimeout
	}
	m := &Manager{
		epoch:         opts.Epoch,
		token:         tok,
		chromePath:    opts.ChromePath,
		headless:      opts.Headless,
		launchTimeout: lt,
		ln:            ln,
		root:          root,
		isAlive:       processAlive,
		sessions:      map[string]*session{},
	}
	m.launch = m.realLaunch
	sweepStaleProfiles(root, m.isAlive)

	mux := http.NewServeMux()
	mux.HandleFunc("/session/", m.handleSession)
	m.srv = &http.Server{Handler: mux}
	go func() { _ = m.srv.Serve(ln) }()
	return m, nil
}

// Addr returns host:port of the loopback listener.
func (m *Manager) Addr() string { return m.ln.Addr().String() }

// Token returns the transport token (secret).
func (m *Manager) Token() string { return m.token }

// Epoch returns the manager's session epoch.
func (m *Manager) Epoch() string { return m.epoch }

func (m *Manager) realLaunch(_ context.Context, _, profileDir string) (*chromeConn, error) {
	return launchChrome(m.chromePath, profileDir, m.headless)
}

// handleSession authenticates (bearer token), then upgrades and pumps. An
// unauthorized handshake gets 401 and NEVER launches Chrome.
func (m *Manager) handleSession(w http.ResponseWriter, r *http.Request) {
	epoch, stageID, ok := parseSessionPath(r.URL.Path)
	if !ok || epoch != m.epoch || stageID == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !m.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error
	}
	ws.SetReadLimit(maxFrameBytes)

	s, err := m.firstConnect(r.Context(), epoch, stageID)
	if err != nil {
		_ = ws.Close()
		return
	}
	s.serveClient(ws)
}

// authorized reports whether the request carries the correct bearer token.
func (m *Manager) authorized(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	got := strings.TrimPrefix(h, prefix)
	return subtle.ConstantTimeCompare([]byte(got), []byte(m.token)) == 1
}

// firstConnect ensures the session for (epoch,stageID) exists and its Chrome is
// launched exactly once (singleflight), bounded by launchTimeout and the caller's
// context. On timeout/cancel the half-started session is torn down.
func (m *Manager) firstConnect(ctx context.Context, epoch, stageID string) (*session, error) {
	key := epoch + "/" + stageID

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, fmt.Errorf("browser: manager closed")
	}
	s, ok := m.sessions[key]
	if !ok {
		s = &session{
			key:        key,
			profileDir: filepath.Join(m.root, profileDirName(epoch, stageID)),
			mgr:        m,
			clients:    map[*wsClient]struct{}{},
			writeCh:    make(chan []byte, writeQueueSize),
			done:       make(chan struct{}),
			launched:   make(chan struct{}),
		}
		m.sessions[key] = s
	}
	m.mu.Unlock()

	launchCtx, cancel := context.WithTimeout(ctx, m.launchTimeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.once.Do(func() {
			conn, err := m.launch(launchCtx, key, s.profileDir)
			s.conn, s.err = conn, err
			if err == nil {
				_ = writeOwnerMarker(s.profileDir, os.Getpid(), m.epoch)
				s.proxy = newProxy()
				go s.writeLoop()
				go s.readFromChrome()
			}
			close(s.launched)
		})
		close(done)
	}()

	select {
	case <-done:
		return s, s.err
	case <-launchCtx.Done():
		m.teardownSession(key)
		return nil, launchCtx.Err()
	}
}

// teardownSession removes and closes a session (used on launch timeout/cancel).
func (m *Manager) teardownSession(key string) {
	m.mu.Lock()
	s := m.sessions[key]
	delete(m.sessions, key)
	m.mu.Unlock()
	if s != nil {
		s.close()
	}
}

// Close is a bounded, idempotent teardown: stop serving, kill owned Chromes, and
// remove owned profile dirs.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	sessions := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = map[string]*session{}
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = m.srv.Shutdown(ctx)
	_ = m.ln.Close()
	for _, s := range sessions {
		s.close()
		os.RemoveAll(s.profileDir)
	}
	return nil
}

// randomToken returns a 32-byte hex transport token.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("browser: token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// NewEpoch returns a host-generated random session epoch ("b-"+8 hex). It is a
// per-process value, NOT a run id: sessions/Chrome never outlive the process that
// created the manager, so a random epoch is both sufficient and the only value
// available at reexec time (the run is resolved later, inside the container).
func NewEpoch() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("browser: epoch: %w", err)
	}
	return "b-" + hex.EncodeToString(b), nil
}

// parseSessionPath parses /session/<epoch>/<stageID>.
func parseSessionPath(p string) (epoch, stageID string, ok bool) {
	rest := strings.TrimPrefix(p, "/session/")
	if rest == p {
		return "", "", false
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// profileDirName builds a flat, filesystem-safe profile dir name under the shared
// owned parent, so the stale-profile sweep can scan a single flat directory.
func profileDirName(epoch, stageID string) string {
	return sanitizeComponent(epoch) + "__" + sanitizeComponent(stageID)
}

func sanitizeComponent(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			return r
		default:
			return '_'
		}
	}, s)
}

// logf logs a manager diagnostic without ever including the token or config.
func logf(format string, args ...any) {
	log.Printf("browser: "+format, args...)
}
