package mcpserver

import (
	"context"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	bus "github.com/akopichin/afm/contracts/events"
	"github.com/akopichin/afm/contracts/state"
)

func updates(t *testing.T, h *harness) (*sdkmcp.ClientSession, <-chan string) {
	t.Helper()
	ch := make(chan string, 16)
	session := h.connect(t, &sdkmcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *sdkmcp.ResourceUpdatedNotificationRequest) {
			ch <- req.Params.URI
		},
	})
	return session, ch
}

func nextUpdate(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case uri := <-ch:
		return uri
	case <-time.After(10 * time.Second):
		t.Fatal("no resource update arrived")
		return ""
	}
}

func TestSubscribeStatusSendsCoalescedUpdate(t *testing.T) {
	h := newHarness(t, "")
	h.run.setStatus("a", state.StatusRunning)
	session, ch := updates(t, h)
	ctx := context.Background()

	if err := session.Subscribe(ctx, &sdkmcp.SubscribeParams{URI: statusURI("run-1")}); err != nil {
		t.Fatal(err)
	}

	h.run.setStatus("a", state.StatusFailed)
	for range 4 {
		h.bus.impulse()
	}
	h.clk.BlockUntil(2)
	h.clk.Advance(coalesceDelay)

	if got := nextUpdate(t, ch); got != statusURI("run-1") {
		t.Fatalf("update for %q", got)
	}
	// The burst produced one notification: a round trip later nothing else is queued.
	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if len(ch) != 0 {
		t.Fatalf("burst was not coalesced: %d extra updates", len(ch))
	}
}

// gatedBus holds the notifier inside Subscribe, so its baseline lands only after the test lets it.
type gatedBus struct {
	*fakeBus
	entered chan struct{}
	gate    chan struct{}
}

func (g *gatedBus) Subscribe(n int) (uint64, <-chan bus.Event) {
	close(g.entered)
	<-g.gate
	return g.fakeBus.Subscribe(n)
}

// The acknowledgement of a subscribe means the baseline exists: a change right after it must be reported.
func TestSubscribeChangeRightAfterAckIsNotified(t *testing.T) {
	gated := &gatedBus{entered: make(chan struct{}), gate: make(chan struct{})}
	h := newHarness(t, "", func(c *Config) {
		gated.fakeBus = c.Events.(*fakeBus)
		c.Events = gated
	})
	h.run.setStatus("a", state.StatusRunning)
	session, ch := updates(t, h)
	go func() {
		<-gated.entered
		time.Sleep(200 * time.Millisecond) // the baseline is still pending while the ack could be sent
		close(gated.gate)
	}()
	if err := session.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: statusURI("run-1")}); err != nil {
		t.Fatal(err)
	}
	h.run.setStatus("a", state.StatusFailed)
	h.clk.BlockUntil(1)
	h.clk.Advance(pollInterval)
	if got := nextUpdate(t, ch); got != statusURI("run-1") {
		t.Fatalf("update for %q", got)
	}
}

func TestSubscribeNegotiatesLegacyVersion(t *testing.T) {
	h := newHarness(t, "")
	session, ch := updates(t, h)
	if v := session.InitializeResult().ProtocolVersion; v >= "2026-07-28" {
		t.Fatalf("the stateful handler cannot serve subscriptions on %s", v)
	}
	if err := session.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: statusURI("run-1")}); err != nil {
		t.Fatal(err)
	}
	h.run.setStatus("a", state.StatusFailed)
	h.clk.Advance(pollInterval)
	if got := nextUpdate(t, ch); got != statusURI("run-1") {
		t.Fatalf("update for %q", got)
	}
}

func TestSubscribeCostResourceUpdates(t *testing.T) {
	cost := &fakeCostSwitch{}
	h := newHarness(t, "", func(c *Config) { c.Cost = cost })
	session, ch := updates(t, h)
	if err := session.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: costURI("run-1")}); err != nil {
		t.Fatal(err)
	}
	cost.set(2000)
	h.clk.Advance(pollInterval)

	if got := nextUpdate(t, ch); got != costURI("run-1") {
		t.Fatalf("update for %q", got)
	}
}

func TestSubscribeRejectsUnknownResource(t *testing.T) {
	h := newHarness(t, "")
	session, _ := updates(t, h)
	for _, uri := range []string{statusURI("other"), planURI("run-1", "plan", "r1"), "afm://nope"} {
		if err := session.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: uri}); err == nil {
			t.Errorf("subscribing to %q must fail", uri)
		}
	}
}

func TestSubscribeWithoutSubscribersStartsNoNotifier(t *testing.T) {
	h := newHarness(t, "")
	session := h.connect(t, nil)
	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	h.svc.mu.Lock()
	started := h.svc.notifying
	h.svc.mu.Unlock()
	if started {
		t.Fatal("reading must not start the notifier")
	}
}

func TestCloseWithOpenSubscriptionDoesNotHang(t *testing.T) {
	h := newHarness(t, "")
	session, _ := updates(t, h)
	if err := session.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: statusURI("run-1")}); err != nil {
		t.Fatal(err)
	}
	h.clk.BlockUntil(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.svc.Close(ctx); err != nil {
		t.Fatalf("Close with a subscription: %v", err)
	}
}
