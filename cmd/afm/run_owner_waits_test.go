//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
	"golang.org/x/sys/unix"
)

func init() {
	// A test run must never write an owner token to the developer's own terminal.
	controllingTerminal = filepath.Join(os.TempDir(), "afm-test-has-no-terminal")
}

// inheritedPipe plays the descriptor an operator's launcher passes down: the write end as a plain number
// without close-on-exec (os.Pipe sets it, a dup does not) and the reader the operator keeps.
func inheritedPipe(t *testing.T) (fd int, reader *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd, err = syscall.Dup(int(w.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	t.Cleanup(func() { r.Close() })
	return fd, r
}

// closedDescriptorNumber is a number nothing holds open: the shape of an AFM_OWNER_TOKEN_FD that was never
// inherited.
func closedDescriptorNumber(t *testing.T) int {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd := int(w.Fd())
	_ = w.Close()
	_ = r.Close()
	return fd
}

func hasCloseOnExec(t *testing.T, fd int) bool {
	t.Helper()
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	return flags&unix.FD_CLOEXEC != 0
}

func requireOwnerTokenVariableGone(t *testing.T) {
	t.Helper()
	if v, set := os.LookupEnv(ownerTokenFDEnv); set {
		t.Fatalf("%s is still set (%q): an agent would inherit it", ownerTokenFDEnv, v)
	}
}

func TestNewOwnerToken_Is256BitsOfRandomnessAndNeverRepeats(t *testing.T) {
	a, err := newOwnerToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newOwnerToken()
	if len(a) != 64 || strings.Trim(a, "0123456789abcdef") != "" {
		t.Fatalf("token %q is not 32 hex-encoded bytes", a)
	}
	if a == b {
		t.Fatal("two tokens are equal")
	}
}

func TestTakeOwnerTokenFD_AcceptsAnInheritedPipeAndSecuresIt(t *testing.T) {
	fd, _ := inheritedPipe(t)
	t.Setenv(ownerTokenFDEnv, strconv.Itoa(fd))

	pipe := takeOwnerTokenFD()

	if pipe == nil || int(pipe.Fd()) != fd {
		t.Fatalf("pipe = %v, want a file over descriptor %d", pipe, fd)
	}
	t.Cleanup(func() { pipe.Close() })
	if !hasCloseOnExec(t, fd) {
		t.Error("the write end must not be inherited by anything afm starts before the delivery")
	}
	requireOwnerTokenVariableGone(t)
}

func TestTakeOwnerTokenFD_RejectsWhatIsNotAnOpenPipeAndLeavesOtherDescriptorsAlone(t *testing.T) {
	for _, value := range []string{"abc", "", "0", "1", "2", "-4", "99999999999999999999"} {
		t.Run("value="+value, func(t *testing.T) {
			t.Setenv(ownerTokenFDEnv, value)
			if got := takeOwnerTokenFD(); got != nil {
				t.Fatalf("accepted %q", value)
			}
			requireOwnerTokenVariableGone(t)
		})
	}
	t.Run("unset", func(t *testing.T) {
		if got := takeOwnerTokenFD(); got != nil {
			t.Fatal("a descriptor appeared from nowhere")
		}
	})
	t.Run("a number nothing holds open", func(t *testing.T) {
		t.Setenv(ownerTokenFDEnv, strconv.Itoa(closedDescriptorNumber(t)))
		if got := takeOwnerTokenFD(); got != nil {
			t.Fatal("accepted a descriptor that was never inherited; afm's own files would reuse the number")
		}
		requireOwnerTokenVariableGone(t)
	})
	// An operator typo that lands on one of afm's own files must neither receive the token nor lose the file.
	t.Run("a regular file", func(t *testing.T) {
		nativeRun(t)
		journal, err := os.CreateTemp(t.TempDir(), "journal")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { journal.Close() })
		t.Setenv(ownerTokenFDEnv, strconv.Itoa(int(journal.Fd())))

		if got := takeOwnerTokenFD(); got != nil {
			t.Fatal("accepted a regular file: the token would sit at rest in it")
		}
		if token := provisionOwnerToken(nil, io.Discard); token != "" {
			t.Fatalf("a token was issued without a channel: %q", token)
		}
		if _, err := journal.WriteString("still open"); err != nil {
			t.Fatalf("the file was closed under its owner: %v", err)
		}
		if raw, _ := os.ReadFile(journal.Name()); string(raw) != "still open" {
			t.Fatalf("file contents = %q", raw)
		}
	})
}

func TestProvisionOwnerToken_DeliversThroughThePrivatePipeOnceAndClosesIt(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	var notice bytes.Buffer

	token := provisionOwnerToken(w, &notice) // takes the write end and closes it after the one delivery

	got, err := io.ReadAll(r) // returns only because the write end was closed
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || string(got) != token+"\n" {
		t.Fatalf("pipe got %q, token %q", got, token)
	}
	if strings.Contains(notice.String(), token) {
		t.Fatalf("the notice printed on stdout must not carry the token: %q", notice.String())
	}
	if !strings.Contains(notice.String(), "descriptor") {
		t.Errorf("notice %q should say how the token was delivered", notice.String())
	}
}

func TestProvisionOwnerToken_DeliversThroughTheControllingTerminal(t *testing.T) {
	nativeRun(t)
	tty := filepath.Join(t.TempDir(), "tty")
	writeEmpty(t, tty)
	pointTerminalAt(t, tty)
	var notice bytes.Buffer

	token := provisionOwnerToken(nil, &notice)

	shown, _ := os.ReadFile(tty)
	if token == "" || !strings.Contains(string(shown), token) {
		t.Fatalf("terminal got %q, token %q", shown, token)
	}
	if strings.Contains(notice.String(), token) {
		t.Fatalf("stdout notice carries the token: %q", notice.String())
	}
	if again := provisionOwnerToken(nil, &notice); again == token {
		t.Fatal("a restart must issue a new token")
	}
}

func TestProvisionOwnerToken_NoChannelMeansNoTokenAndANonSecretExplanation(t *testing.T) {
	nativeRun(t)
	var notice bytes.Buffer

	token := provisionOwnerToken(nil, &notice)

	if token != "" {
		t.Fatalf("token %q was issued without a way to hand it over", token)
	}
	for _, want := range []string{"unavailable", ownerTokenFDEnv} {
		if !strings.Contains(notice.String(), want) {
			t.Errorf("explanation %q lacks %q", notice.String(), want)
		}
	}
}

// A pipe that cannot be written (closed, or the reader's end) is no reason to fall back to another channel.
func TestProvisionOwnerToken_ABrokenDescriptorDoesNotFallBackToAnOpenRoute(t *testing.T) {
	nativeRun(t)
	tty := filepath.Join(t.TempDir(), "tty")
	writeEmpty(t, tty)
	pointTerminalAt(t, tty)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	_ = w.Close() // a closed pipe: the shape of a descriptor the operator already took away
	var notice bytes.Buffer

	if token := provisionOwnerToken(w, &notice); token != "" {
		t.Fatalf("token %q issued although the descriptor could not be written", token)
	}
	if shown, _ := os.ReadFile(tty); len(shown) != 0 {
		t.Fatalf("the terminal received %q after the descriptor failed", shown)
	}
	if !strings.Contains(notice.String(), "unavailable") {
		t.Errorf("notice %q", notice.String())
	}
}

// Inside afm's own Docker container /dev/tty is the container's stdout, which the host copies into its log and
// `docker logs`: no token is shown there, the routes stay closed and the notice says why.
func TestProvisionOwnerToken_InADockerReExecNeverUsesTheTerminal(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	tty := filepath.Join(t.TempDir(), "tty")
	writeEmpty(t, tty)
	pointTerminalAt(t, tty)
	var notice bytes.Buffer

	token := provisionOwnerToken(nil, &notice)

	if token != "" {
		t.Fatalf("token %q issued inside the container", token)
	}
	if shown, _ := os.ReadFile(tty); len(shown) != 0 {
		t.Fatalf("the container terminal received %q", shown)
	}
	if !strings.Contains(notice.String(), "owner wait routes need a native run") {
		t.Errorf("notice %q must explain the limitation", notice.String())
	}
}

// The host hands the run to a container: the descriptor channel does not cross that boundary, so it is closed
// before `docker run` and the operator is told.
func TestReleaseOwnerTokenForDocker_ClosesTheDescriptorAndSaysItIsNotSupported(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	old := ownerTokenPipe
	ownerTokenPipe = w
	t.Cleanup(func() { ownerTokenPipe = old })
	var out bytes.Buffer

	releaseOwnerTokenForDocker(&out)

	if err := w.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closing the descriptor again = %v, want os.ErrClosed: it must have been closed already", err)
	}
	if ownerTokenPipe != nil {
		t.Error("the descriptor must be spent")
	}
	if !strings.Contains(out.String(), ownerTokenFDEnv) || !strings.Contains(out.String(), "Docker") {
		t.Errorf("output %q must say the descriptor channel is not supported in Docker mode", out.String())
	}

	out.Reset()
	releaseOwnerTokenForDocker(&out) // nothing was given: nothing to say
	if out.Len() != 0 {
		t.Errorf("unexpected output without a descriptor: %q", out.String())
	}
}

// A run without a dashboard (port <= 0) has nobody to give the token to: the operator's pipe is closed, not held.
func TestStartDashboard_WithoutAPortClosesTheTokenPipe(t *testing.T) {
	_, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := ownerTokenPipe
	ownerTokenPipe = w
	t.Cleanup(func() { ownerTokenPipe = old })
	port := 0

	srv, err := startDashboard(config.Config{Server: config.ServerConfig{Port: &port}}, server.Config{}, nil, true)

	if srv != nil || err != nil {
		t.Fatalf("startDashboard = %v, %v; want no dashboard and no error", srv, err)
	}
	if err := w.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closing the descriptor again = %v, want os.ErrClosed: it must have been closed already", err)
	}
	if ownerTokenPipe != nil {
		t.Error("the descriptor must be spent")
	}
}

// The token and the variable that carried the descriptor reach neither the environment nor any process afm
// starts: a real child sees no variable and cannot write to the pipe.
func TestProvisionOwnerToken_ReachesNoChildProcess(t *testing.T) {
	fd, r := inheritedPipe(t)
	t.Setenv(ownerTokenFDEnv, strconv.Itoa(fd))
	pipe := takeOwnerTokenFD()
	if pipe == nil {
		t.Fatal("the pipe was rejected")
	}

	// A child started before the dashboard — Chrome, a hook, git — gets the process environment and descriptors.
	out, err := exec.Command("sh", "-c", fmt.Sprintf("echo planted >&%d; env", fd)).CombinedOutput() //nolint:gosec // test-built command
	if err != nil {
		t.Logf("the child could not write to the descriptor, as intended: %v", err)
	}
	token := provisionOwnerToken(pipe, io.Discard)

	if strings.Contains(string(out), ownerTokenFDEnv) || strings.Contains(string(out), token) {
		t.Fatalf("the child environment carries the owner token channel:\n%s", out)
	}
	got, _ := io.ReadAll(r)
	if string(got) != token+"\n" {
		t.Fatalf("pipe got %q: a child wrote into the operator's channel, or the token was lost", got)
	}
	for _, kv := range os.Environ() {
		if strings.Contains(kv, token) {
			t.Fatalf("the environment carries the token in %q", strings.SplitN(kv, "=", 2)[0])
		}
	}
}

func writeEmpty(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// nativeRun makes the test independent of where it runs: inside afm's own container no terminal channel exists.
func nativeRun(t *testing.T) {
	t.Helper()
	t.Setenv("AFM_IN_DOCKER", "")
}

func pointTerminalAt(t *testing.T, path string) {
	t.Helper()
	old := controllingTerminal
	controllingTerminal = path
	t.Cleanup(func() { controllingTerminal = old })
}

// newOwnerWaitAdapter is a real orchestrator and store behind the owner adapter.
func newOwnerWaitAdapter(t *testing.T) (ownerWaitActions, *orchestrator.Orchestrator) {
	t.Helper()
	a, _ := newMeshAdapterTestActions(t, "impl", "docs")
	return ownerWaitActions(a), a.orch
}

func registerStageWait(t *testing.T, orch *orchestrator.Orchestrator, stage, op, thread string) agentcomm.WaitID {
	t.Helper()
	deadline := time.Now().Add(time.Hour)
	receipt, err := orch.Wait(context.Background(), orchestrator.WaitRequest{
		OperationID: op, Waiter: agentcomm.ParticipantID("stage:" + stage), Deadline: &deadline,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: agentcomm.ThreadID(thread)},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return receipt.WaitID
}

func TestOwnerWaitActions_ListAndCancelOnlyTheStagesOwnWaits(t *testing.T) {
	a, orch := newOwnerWaitAdapter(t)
	ctx := context.Background()
	w1 := registerStageWait(t, orch, "impl", "op-w1", "t-one")
	w2 := registerStageWait(t, orch, "impl", "op-w2", "t-two")
	foreign := registerStageWait(t, orch, "docs", "op-w3", "t-three")

	listed, err := a.StageWaits(ctx, "impl")
	if err != nil || len(listed) != 2 || listed[0].WaitID != w1 || listed[1].WaitID != w2 {
		t.Fatalf("StageWaits = %+v, %v; want impl's two waits only", listed, err)
	}

	_, err = a.CancelStageWaits(ctx, "req-1", "impl", server.OwnerCancelWaitsRequest{WaitIDs: []string{string(w1), string(foreign)}})
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeNotFound {
		t.Fatalf("a wait of another stage: err = %v, want not_found", err)
	}

	got, err := a.CancelStageWaits(ctx, "req-2", "impl", server.OwnerCancelWaitsRequest{WaitIDs: []string{string(w1), string(w2)}, Reason: "stale"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Status != agentcomm.WaitCancelled || got[1].Status != agentcomm.WaitCancelled || got[0].Reason != "stale" {
		t.Fatalf("result = %+v, want both cancelled with the reason", got)
	}
	again, err := a.CancelStageWaits(ctx, "req-2", "impl", server.OwnerCancelWaitsRequest{WaitIDs: []string{string(w1), string(w2)}, Reason: "stale"})
	if err != nil || len(again) != 2 || again[0].Status != agentcomm.WaitCancelled {
		t.Fatalf("repeat of req-2 = %+v, %v; want the same outcome", again, err)
	}
	other, _ := a.StageWaits(ctx, "docs")
	if len(other) != 1 || other[0].Status != agentcomm.WaitPending {
		t.Fatalf("docs' wait was touched: %+v", other)
	}
}

// The owner's request id belongs to the stage it was first used for. Without that, repeating a successful
// request against another stage replays the first result and skips the ownership check.
func TestOwnerWaitActions_ARequestIdRepeatedAgainstAnotherStageIsNotAReplay(t *testing.T) {
	a, orch := newOwnerWaitAdapter(t)
	ctx := context.Background()
	mine := registerStageWait(t, orch, "impl", "op-mine", "t-mine")
	theirs := registerStageWait(t, orch, "docs", "op-theirs", "t-theirs")
	req := server.OwnerCancelWaitsRequest{WaitIDs: []string{string(mine)}, Reason: "stale"}
	if _, err := a.CancelStageWaits(ctx, "req-1", "impl", req); err != nil {
		t.Fatal(err)
	}

	_, err := a.CancelStageWaits(ctx, "req-1", "docs", req)

	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeNotFound {
		t.Fatalf("the same request against docs: err = %v, want not_found", err)
	}
	if docs, _ := a.StageWaits(ctx, "docs"); len(docs) != 1 || docs[0].WaitID != theirs || docs[0].Status != agentcomm.WaitPending {
		t.Fatalf("docs' wait was touched: %+v", docs)
	}
}

// The owner's request ids are their own namespace: an id that an agent or the UI already used for another
// operation is still free for a cancellation.
func TestOwnerWaitActions_ARequestIdUsedByAnotherOperationDoesNotConflict(t *testing.T) {
	a, orch := newOwnerWaitAdapter(t)
	w := registerStageWait(t, orch, "impl", "req-shared", "t-shared") // the Wait operation committed under "req-shared"

	got, err := a.CancelStageWaits(context.Background(), "req-shared", "impl", server.OwnerCancelWaitsRequest{WaitIDs: []string{string(w)}})

	if err != nil || len(got) != 1 || got[0].Status != agentcomm.WaitCancelled {
		t.Fatalf("cancel under a request id another operation used: %+v, %v", got, err)
	}
}

// startOwnerTestDashboard starts the real dashboard via startDashboard with the owner token delivered into a pipe.
func startOwnerTestDashboard(t *testing.T) (baseURL, token string, orch *orchestrator.Orchestrator) {
	t.Helper()
	a, store := newMeshAdapterTestActions(t, "impl")
	orch = a.orch
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	cfg := config.Default()
	cfg.Server.Port = &port

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	old := ownerTokenPipe
	ownerTokenPipe = w
	t.Cleanup(func() { ownerTokenPipe = old })

	srv, err := startDashboard(cfg, server.Config{RunDir: t.TempDir(), Store: store, Stages: registryStageProvider{orch: orch}}, orch, false)
	if err != nil {
		t.Fatalf("startDashboard: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if ownerTokenPipe != nil {
		t.Error("the descriptor must be spent after the first dashboard")
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port), strings.TrimSpace(string(raw)), orch
}

func ownerRequest(t *testing.T, method, url, token string, body any) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if method == http.MethodPost {
		req.Header.Set("Origin", "http://"+req.Host)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// End to end through the real dashboard: the delivered token opens exactly the two owner routes, and appears
// nowhere a browser, an agent or a log could read it.
func TestStartDashboard_OwnerTokenOpensTheOwnerRoutesAndLeaksNowhere(t *testing.T) {
	base, token, orch := startOwnerTestDashboard(t)
	if len(token) != 64 {
		t.Fatalf("delivered token %q", token)
	}
	wait := registerStageWait(t, orch, "impl", "op-e2e", "t-e2e")

	if code, _ := ownerRequest(t, http.MethodGet, base+"/api/mesh/stages/impl/waits", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no token: HTTP %d, want 401", code)
	}
	if code, _ := ownerRequest(t, http.MethodGet, base+"/api/mesh/stages/impl/waits", "wrong-"+token, nil); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: HTTP %d, want 401", code)
	}
	code, body := ownerRequest(t, http.MethodGet, base+"/api/mesh/stages/impl/waits", token, nil)
	if code != http.StatusOK || !strings.Contains(body, string(wait)) {
		t.Fatalf("owner GET: HTTP %d %s", code, body)
	}
	code, body = ownerRequest(t, http.MethodPost, base+"/api/mesh/stages/impl/waits/cancel", token,
		map[string]any{"request_id": "req-e2e", "wait_ids": []string{string(wait)}, "reason": "stale"})
	if code != http.StatusOK || !strings.Contains(body, `"cancelled"`) {
		t.Fatalf("owner POST: HTTP %d %s", code, body)
	}

	for _, path := range []string{"/", "/api/status", "/api/events", "/api/mesh/participants", "/api/mesh/board"} {
		_, page := ownerRequest(t, http.MethodGet, base+path, "", nil)
		if strings.Contains(page, token) {
			t.Errorf("GET %s leaks the owner token", path)
		}
	}
}

// With no way to hand the token over the routes do not exist, even for a caller that guesses a bearer.
func TestStartDashboard_WithoutADeliveryChannelTheOwnerRoutesStayClosed(t *testing.T) {
	a, store := newMeshAdapterTestActions(t, "impl")
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	cfg := config.Default()
	cfg.Server.Port = &port
	old := ownerTokenPipe
	ownerTokenPipe = nil
	t.Cleanup(func() { ownerTokenPipe = old })

	srv, err := startDashboard(cfg, server.Config{RunDir: t.TempDir(), Store: store, Stages: registryStageProvider(a)}, a.orch, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	code, _ := ownerRequest(t, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/mesh/stages/impl/waits", port), "anything", nil)
	if code != http.StatusNotFound {
		t.Fatalf("HTTP %d, want 404 while no token exists", code)
	}
}
