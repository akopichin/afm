//go:build unix

package serve

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/contracts/state"
)

const run1Status = "afm://runs/run-1/status"

func waitEntered(t *testing.T, g *callGate, what string) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never reached the run", what)
	}
}

func joinCall(t *testing.T, done <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatalf("%s did not return", what)
		return nil
	}
}

// notAnsweredWhileHeld checks that a call waiting behind a held transition has not been answered yet.
func notAnsweredWhileHeld(t *testing.T, done chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		t.Errorf("%s was answered (%v) while an earlier subscription change was still in flight", what, err)
		done <- err
	case <-time.After(300 * time.Millisecond):
	}
}

func wantUpdate(t *testing.T, updates <-chan string, uri string) {
	t.Helper()
	select {
	case got := <-updates:
		if got != uri {
			t.Errorf("update for %s, want %s", got, uri)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no update for %s reached the subscriber", uri)
	}
}

// subscribeHeld subscribes in the background while held may stop it inside the host. On every exit of the test the
// held subscribe is released and joined before its client closes: Close waits for the subscribe to return.
func subscribeHeld(t *testing.T, cs *sdkmcp.ClientSession, held *callGate) (chan error, func()) {
	t.Helper()
	release := sync.OnceFunc(func() { close(held.release) })
	subscribed := make(chan error, 1)
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		subscribed <- cs.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: run1Status})
	}()
	t.Cleanup(func() {
		release()
		select {
		case <-returned:
			_ = cs.Close()
		case <-time.After(15 * time.Second):
			t.Error("the held subscribe did not return after its release; its client is left open")
		}
	})
	return subscribed, release
}

func subscribeAsync(cs *sdkmcp.ClientSession, uri string) chan error {
	done := make(chan error, 1)
	go func() { done <- cs.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: uri}) }()
	return done
}

// The last unsubscribe is still on its way to the run when another client subscribes: the new subscription must be
// made after it, or the run drops it and the client never hears of a change.
func TestLastUnsubscribeCannotUndoANewerSubscribe(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	if err := f.client.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: run1Status}); err != nil {
		t.Fatal(err)
	}
	held := child.gate("resources/unsubscribe", false)
	unsubscribed := make(chan error, 1)
	go func() {
		unsubscribed <- f.client.Unsubscribe(context.Background(), &sdkmcp.UnsubscribeParams{URI: run1Status})
	}()
	waitEntered(t, held, "the last unsubscribe")

	updates := make(chan string, 8)
	second := f.connectNotifying(updates)
	t.Cleanup(func() { _ = second.Close() })
	subscribed := subscribeAsync(second, run1Status)
	notAnsweredWhileHeld(t, subscribed, "the new subscribe")
	close(held.release)
	if err := joinCall(t, unsubscribed, "the unsubscribe"); err != nil {
		t.Fatal(err)
	}
	if err := joinCall(t, subscribed, "the subscribe"); err != nil {
		t.Fatal(err)
	}

	child.setStatus(state.RunStatusFinished)
	wantUpdate(t, updates, run1Status)
}

// The same race through a host client that disconnects: its release must not undo a newer subscribe either.
func TestReleasedHostCannotUndoANewerSubscribe(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	leaving := f.connectNotifying(make(chan string, 8))
	if err := leaving.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: run1Status}); err != nil {
		t.Fatal(err)
	}
	held := child.gate("resources/unsubscribe", false)
	_ = leaving.Close()
	waitEntered(t, held, "the release of the disconnected client")

	subscribed := subscribeAsync(f.client, run1Status)
	notAnsweredWhileHeld(t, subscribed, "the new subscribe")
	close(held.release)
	if err := joinCall(t, subscribed, "the subscribe"); err != nil {
		t.Fatal(err)
	}

	child.setStatus(state.RunStatusFinished)
	wantUpdate(t, f.updates, run1Status)
}

// The first subscriber's request to the run fails while a second one waits: the second must not be told it
// subscribed to a subscription the run does not have.
func TestFailedFirstSubscribeLeavesNoFollowerWithoutTheRun(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	if _, isErr := f.call("afm_run_status", map[string]any{"run_id": "run-1"}); isErr {
		t.Fatal("warm-up status failed")
	}
	held := child.gate("resources/subscribe", true)
	first := subscribeAsync(f.client, run1Status)
	waitEntered(t, held, "the first subscribe")

	updates := make(chan string, 8)
	second := f.connectNotifying(updates)
	t.Cleanup(func() { _ = second.Close() })
	following := subscribeAsync(second, run1Status)
	notAnsweredWhileHeld(t, following, "the second subscribe")
	close(held.release)
	if err := joinCall(t, first, "the failed subscribe"); err == nil {
		t.Error("the subscribe whose request broke must fail")
	}
	if err := joinCall(t, following, "the second subscribe"); err != nil {
		t.Fatal(err)
	}

	child.setStatus(state.RunStatusFinished)
	wantUpdate(t, updates, run1Status)
}

// Closing the host ends a subscription change that the run does not answer.
func TestCloseCancelsABlockedSubscription(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	held := child.gate("resources/subscribe", false)
	t.Cleanup(func() { close(held.release) })
	subscribed := subscribeAsync(f.client, run1Status)
	waitEntered(t, held, "the subscribe")

	closed := make(chan struct{})
	go func() { f.gw.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(closeGrace + 3*time.Second):
		t.Fatal("Gateway.Close waits for a subscription the run never answers")
	}
	if err := joinCall(t, subscribed, "the blocked subscribe"); err == nil {
		t.Error("a subscribe cut off by the shutdown must fail")
	}
}

// A repeat cycle is another run: a new session to it must not subscribe to the resources of the old run.
func TestRepeatDoesNotCarryOldRunSubscriptions(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	if err := f.client.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: run1Status}); err != nil {
		t.Fatal(err)
	}
	child.nextCycle("run-2")
	wantUpdate(t, f.updates, run1Status) // the lost session tells the subscriber to look again
	if body, isErr := f.call("afm_run_status", map[string]any{"run_id": "run-2"}); isErr {
		t.Fatalf("status of the new cycle = %v", body)
	}
	if n := child.count("resources/subscribe"); n != 1 {
		t.Errorf("the run was asked to subscribe %d times, want only the original subscription to run-1", n)
	}
}

// A session that was lost is replaced by one that subscribes again to what its clients still want.
func TestReconnectReplaysSubscriptionsOfTheSameRun(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	if err := f.client.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: run1Status}); err != nil {
		t.Fatal(err)
	}
	f.sessions.retire(liveSession(f))
	wantUpdate(t, f.updates, run1Status) // told to look again: its subscription lived on the lost session
	if body, isErr := f.call("afm_run_status", map[string]any{"run_id": "run-1"}); isErr {
		t.Fatalf("status after the session was lost = %v", body)
	}
	if n := child.count("resources/subscribe"); n != 2 {
		t.Errorf("subscribes = %d, want the replay on the new session", n)
	}
	child.setStatus(state.RunStatusFinished)
	wantUpdate(t, f.updates, run1Status)
}

// liveSession is the host's only session to a run.
func liveSession(f *hostFixture) *childSession {
	f.sessions.mu.Lock()
	defer f.sessions.mu.Unlock()
	for _, sess := range f.sessions.sessions {
		return sess
	}
	f.t.Fatal("the host has no session to the run")
	return nil
}

// The run's session is lost after a subscribe was made on it but before the SDK recorded the new subscriber, so
// the loss is broadcast without it. The subscriber must still hear of it, or it waits on a session that is gone.
func TestLossBeforeTheSubscriberIsRecordedReachesIt(t *testing.T) {
	for name, joined := range map[string]bool{"first subscriber": false, "joined while another subscriber exists": true} {
		t.Run(name, func(t *testing.T) {
			f := newHostFixture(t, AllHostGroups())
			broadcast := make(chan string, 64)
			notify := f.sessions.Updated
			f.sessions.Updated = func(uri string) { notify(uri); broadcast <- uri }
			var hold atomic.Pointer[callGate]
			f.gw.subscribed = func() {
				if g := hold.Swap(nil); g != nil {
					close(g.entered)
					<-g.release
				}
			}
			child := newFakeChild(t, testInstance, "run-1")
			f.managed(child, "req")
			if joined {
				if err := f.client.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: run1Status}); err != nil {
					t.Fatal(err)
				}
			}
			held := &callGate{entered: make(chan struct{}), release: make(chan struct{})}
			hold.Store(held)

			updates := make(chan string, 8)
			second := f.connectNotifying(updates)
			subscribed, release := subscribeHeld(t, second, held)
			select {
			case <-held.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("the subscribe was never made")
			}
			f.sessions.retire(liveSession(f))
			for sent := ""; sent != run1Status; {
				select {
				case sent = <-broadcast:
				case <-time.After(10 * time.Second):
					t.Fatal("the loss was never broadcast")
				}
			}
			release()
			if err := joinCall(t, subscribed, "the subscribe"); err != nil {
				t.Fatal(err)
			}
			wantUpdate(t, updates, run1Status)

			// Looking again reaches the run through a new session, which subscribes for the client again.
			if body, isErr := callOn(t, second, "afm_run_status", map[string]any{"run_id": "run-1"}); isErr {
				t.Fatalf("status after the loss = %v", body)
			}
			if n := child.count("resources/subscribe"); n != 2 {
				t.Errorf("subscribes = %d, want the subscription made again on the new session", n)
			}
			child.setStatus(state.RunStatusFinished)
			wantUpdate(t, updates, run1Status)
		})
	}
}

// A subscribe and an unsubscribe of the same client and URI meet: the subscribe already reached the run, but the SDK
// has not recorded its subscriber yet. The unsubscribe must come after that record, or the SDK keeps a subscriber
// the run no longer has, and the loss of the run's session never reaches it.
func TestUnsubscribeWaitsForTheSubscribeItMeets(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	held := &callGate{entered: make(chan struct{}), release: make(chan struct{})}
	var hold atomic.Pointer[callGate]
	hold.Store(held)
	f.gw.subscribed = func() {
		if g := hold.Swap(nil); g != nil {
			close(g.entered)
			<-g.release
		}
	}
	subscribed, release := subscribeHeld(t, f.client, held)
	waitEntered(t, held, "the subscribe")

	unsubscribed := make(chan error, 1)
	go func() {
		unsubscribed <- f.client.Unsubscribe(context.Background(), &sdkmcp.UnsubscribeParams{URI: run1Status})
	}()
	notAnsweredWhileHeld(t, unsubscribed, "the unsubscribe")
	release()
	if err := joinCall(t, subscribed, "the subscribe"); err != nil {
		t.Fatal(err)
	}
	if err := joinCall(t, unsubscribed, "the unsubscribe"); err != nil {
		t.Fatal(err)
	}

	f.sessions.mu.Lock()
	members := len(f.sessions.subs[InstanceIDFor(LaunchID("req"))][run1Status])
	f.sessions.mu.Unlock()
	var routed atomic.Int32
	f.gw.server.AddSendingMiddleware(func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
		return func(ctx context.Context, method string, req sdkmcp.Request) (sdkmcp.Result, error) {
			if method == "notifications/resources/updated" {
				routed.Add(1)
			}
			return next(ctx, method, req)
		}
	})
	f.gw.resourceUpdated(run1Status)
	if routed.Load() != 0 || members != 0 {
		t.Errorf("after the unsubscribe the SDK still notifies %d subscribers; the run's subscription has %d members",
			routed.Load(), members)
	}
}

// parkWait forwards an afm_run_wait that stays on the run until the test ends: a call unrelated to subscriptions
// that keeps the run's session busy.
func parkWait(t *testing.T, f *hostFixture, child *fakeChild) {
	t.Helper()
	status, _ := f.call("afm_run_status", map[string]any{"run_id": "run-1"})
	cursor, _ := status["cursor"].(string)
	ctx, cancel := context.WithCancel(context.Background())
	parked := make(chan struct{})
	go func() {
		defer close(parked)
		_, _ = f.client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "afm_run_wait",
			Arguments: map[string]any{"run_id": "run-1", "cursor": cursor, "timeout_seconds": 120}})
	}()
	t.Cleanup(func() { cancel(); <-parked })
	eventually(t, "the wait to reach the run", func() bool { return child.count("tool:afm_run_wait") == 1 })
}

// A subscription change whose request to the run breaks gives up the run's session, but does not wait for the
// other calls still running on it: they may last minutes, and every subscription change waits for this one.
func TestBrokenSubscriptionChangeDoesNotWaitForOtherCalls(t *testing.T) {
	changes := map[string]struct {
		method          string
		subscribedFirst bool
		// change makes the subscription change whose request to the run breaks.
		change func(f *hostFixture)
	}{
		"unsubscribe": {"resources/unsubscribe", true, func(f *hostFixture) {
			_ = f.client.Unsubscribe(context.Background(), &sdkmcp.UnsubscribeParams{URI: run1Status})
		}},
		"subscribe": {"resources/subscribe", false, func(f *hostFixture) {
			_ = f.client.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: run1Status})
		}},
		"release of a disconnected client": {"resources/unsubscribe", false, func(f *hostFixture) {
			leaving := f.connectNotifying(make(chan string, 8))
			if err := leaving.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: run1Status}); err != nil {
				f.t.Error(err)
			}
			_ = leaving.Close()
		}},
	}
	for name, tc := range changes {
		t.Run(name, func(t *testing.T) {
			f := newHostFixture(t, AllHostGroups())
			child := newFakeChild(t, testInstance, "run-1")
			f.managed(child, "req")
			if tc.subscribedFirst {
				if err := f.client.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: run1Status}); err != nil {
					t.Fatal(err)
				}
			}
			parkWait(t, f, child)
			broken := child.gate(tc.method, true)
			close(broken.release)
			done := make(chan struct{})
			go func() { tc.change(f); close(done) }()
			waitEntered(t, broken, "the broken change")
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the broken subscription change waited for an unrelated call on the run")
			}

			updates := make(chan string, 8)
			second := f.connectNotifying(updates)
			t.Cleanup(func() { _ = second.Close() })
			subscribed := make(chan error, 1)
			go func() { subscribed <- second.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: run1Status}) }()
			select {
			case err := <-subscribed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the next subscribe waited behind the broken change")
			}
			child.setStatus(state.RunStatusFinished)
			wantUpdate(t, updates, run1Status)
		})
	}
}

// A control call whose connection broke gives up the run's session: its subscribers are told to look again, and
// the session that replaces it subscribes again.
func TestBrokenCallTellsSubscribersTheirSessionWasLost(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	if err := f.client.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: run1Status}); err != nil {
		t.Fatal(err)
	}
	child.mu.Lock()
	child.dropTool = "afm_stage_pause"
	child.mu.Unlock()
	if body, isErr := f.call("afm_stage_pause", map[string]any{"run_id": "run-1", "stage_id": "build"}); !isErr || body["code"] != codeOutcomeUnknown {
		t.Fatalf("broken pause = %v", body)
	}
	wantUpdate(t, f.updates, run1Status)

	if body, isErr := f.call("afm_run_status", map[string]any{"run_id": "run-1"}); isErr {
		t.Fatalf("status after the loss = %v", body)
	}
	if n := child.count("resources/subscribe"); n != 2 {
		t.Errorf("subscribes = %d, want the subscription made again on the new session", n)
	}
	child.setStatus(state.RunStatusFinished)
	wantUpdate(t, f.updates, run1Status)
}

// The run refuses a subscription that a new session replays: nobody may stay subscribed without the run's
// subscription, so its subscribers are told and the next subscriber asks the run again.
func TestRefusedReplayLeavesNoSubscriberWithoutTheRun(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	if err := f.client.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: run1Status}); err != nil {
		t.Fatal(err)
	}
	refused := child.gate("resources/subscribe", false)
	refused.refuse = true
	close(refused.release)
	child.mu.Lock()
	child.dropTool = "afm_events_read" // read-only: the host retries it on a new session, which replays
	child.mu.Unlock()
	if body, isErr := f.call("afm_events_read", map[string]any{"run_id": "run-1"}); isErr {
		t.Fatalf("read after the loss = %v", body)
	}
	if n := child.count("resources/subscribe"); n != 2 {
		t.Fatalf("subscribes = %d, want the original and the refused replay", n)
	}
	wantUpdate(t, f.updates, run1Status)

	updates := make(chan string, 8)
	second := f.connectNotifying(updates)
	t.Cleanup(func() { _ = second.Close() })
	if err := second.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: run1Status}); err != nil {
		t.Fatal(err)
	}
	if n := child.count("resources/subscribe"); n != 3 {
		t.Errorf("subscribes = %d: the new subscriber was told it subscribed without asking the run", n)
	}
	child.setStatus(state.RunStatusFinished)
	wantUpdate(t, updates, run1Status)
}
