package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/contracts/clock"
	bus "github.com/akopichin/afm/contracts/events"
)

const (
	// coalesceDelay folds a burst of bus events into one recompute.
	coalesceDelay = 250 * time.Millisecond
	// pollInterval recomputes without any event: questions arrive as files and plan.md edits publish nothing,
	// and the bus drops events for a slow subscriber.
	pollInterval = 2 * time.Second
	notifierBuf  = 64
)

// ensureNotifier starts the single notifier goroutine on first use and returns once its baseline is taken, so a
// change after the caller's acknowledgement is never swallowed. It fails once the service is closing.
func (s *Service) ensureNotifier() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errClosed
	}
	if !s.notifying {
		s.notifying = true
		s.wg.Add(1)
		go s.runNotifier()
	}
	s.mu.Unlock()

	select {
	case <-s.ready:
		return nil
	case <-s.done:
		return errClosed
	case <-s.ctx.Done():
		return errClosed
	}
}

// changedChan is closed the next time the attention model changes. Take it before reading the model, so a change
// between the read and the wait still wakes the waiter.
func (s *Service) changedChan() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

// runNotifier recomputes the attention model on a bus impulse, on a dropped-event count that grew, and on a
// timer tick, then wakes every waiter and tells subscribers.
func (s *Service) runNotifier() {
	defer s.wg.Done()
	var (
		events  <-chan bus.Event
		id      uint64
		dropped uint64
	)
	if s.cfg.Events != nil {
		id, events = s.cfg.Events.Subscribe(notifierBuf)
		defer s.cfg.Events.Unsubscribe(id)
	}
	// The baseline comes first; readiness is announced only once the ticker is registered too.
	s.refresh(s.ctx)
	ticker := s.clk().NewTicker(pollInterval)
	defer ticker.Stop()
	close(s.ready)

	var (
		timer  clock.Timer
		timerC <-chan time.Time
	)
	arm := func() {
		if timer == nil {
			timer = s.clk().NewTimer(coalesceDelay)
		} else if timerC == nil {
			timer.Reset(coalesceDelay)
		}
		timerC = timer.C()
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	// Lost events leave nothing to coalesce, so a grown drop count refreshes at once.
	droppedGrew := func() bool {
		if s.cfg.Events == nil {
			return false
		}
		n := s.cfg.Events.SubscriberDroppedCount(id)
		grew := n > dropped
		dropped = n
		return grew
	}

	for {
		select {
		case <-s.done:
			return
		case _, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if droppedGrew() {
				timerC = nil
				s.refresh(s.ctx)
				continue
			}
			arm()
		case <-timerC:
			timerC = nil
			s.refresh(s.ctx)
		case <-ticker.C():
			droppedGrew()
			s.refresh(s.ctx)
		}
	}
}

// refresh recomputes the model, broadcasts a change to waiters and notifies resource subscribers.
func (s *Service) refresh(ctx context.Context) {
	cursor := s.cursorOf(s.buildAttention(ctx, s.cfg.Reader.Snapshot()))
	cost := digestOf(s.costNow())

	s.mu.Lock()
	first := s.lastCursor == ""
	changed := cursor != s.lastCursor
	costChanged := cost != s.lastCost
	s.lastCursor, s.lastCost = cursor, cost
	var wake chan struct{}
	if changed {
		wake, s.changed = s.changed, make(chan struct{})
	}
	s.mu.Unlock()

	if wake != nil {
		close(wake)
	}
	if first {
		return
	}
	if changed || costChanged {
		_ = s.server.ResourceUpdated(ctx, &sdkmcp.ResourceUpdatedNotificationParams{URI: statusURI(s.runID)})
	}
	if costChanged {
		_ = s.server.ResourceUpdated(ctx, &sdkmcp.ResourceUpdatedNotificationParams{URI: costURI(s.runID)})
	}
}

func digestOf(v any) string {
	raw, _ := json.Marshal(v) // plain data; cannot fail
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}
