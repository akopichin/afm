// Package watch implements the Этап 5 "watcher" collector: deciding WHEN an advisory helper task that
// observes the main stage agent's own output should get its next turn — filtering noise, coalescing bursts
// into one batch, and bounding how many turns (and therefore subprocesses) a long, chatty stage run can ever
// cause (dev-plan.md's T5.3 "TestCollector_HundredsOfEventsDoNotSpawnHundredsOfProcesses").
//
// This package deliberately knows nothing about pkg/orchestrator's task lifecycle (state.Operation/Effect,
// CAS, etc.) — it only ever reports "is a turn ready, and on what observations" (Ready) and "did a turn just
// end" (SetBusy(false)); every actual state mutation is still a normal assistants.Task transition applied
// through the EXISTING lifecycle (pkg/orchestrator/assistants_lifecycle.go's runHelperTask /
// pkg/orchestrator/assistants.go's maybeScheduleTask) — Этап 5 adds no second task-running mechanism.
package watch

import (
	"sync"
	"time"

	"github.com/akopichin/afm/pkg/executor"
)

// Clock is the injectable time seam — mirrors the fingerprintFn/projectDir seam convention used elsewhere
// in this codebase (pkg/orchestrator/orchestrator.go) so Collector behavior is deterministic and testable
// without real sleeps (TestCollector_DeterministicWithFakeClock).
type Clock interface {
	Now() time.Time
}

// RealClock is the production Clock.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// Config bundles the Stage 5 numeric defaults for one Collector.
//
// IMPORTANT (dev-plan.md T5.3's acceptance criterion): these numbers are ideally tuned against a real recorded
// AFM event stream (one live run captured as a fixture). No such capture was run in the session that produced
// this code — the values below are conservative, explicitly-labelled placeholders chosen to make the
// documented invariants (no runaway spawn rate, no invocation on pure noise) hold comfortably, not numbers
// backed by a real measurement. Revisit them once a real stream is captured, per dev-plan.md's own note.
type Config struct {
	// Debounce — how long to wait after the LAST new observation before considering a batch "settled" enough
	// to fire. Chosen so a burst of several tool_use/text blocks from one model turn (which typically lands
	// within a second or two of each other) coalesces into ONE batch instead of firing mid-burst.
	Debounce time.Duration
	// Cooldown — minimum spacing between the end of one watcher turn and the start of the next, independent of
	// Debounce — bounds the turn RATE even if the main agent is a constant, never-settling stream of output.
	Cooldown time.Duration
	// MaxBatch caps how many observations are bundled into one checkpoint context package — keeps a single
	// pass's prompt (and the durable checkpoint file, T5.2) bounded regardless of how long the main agent's
	// been running.
	MaxBatch int
	// MinNewContentBytes — a batch must carry at least this much total observation content before it's
	// considered "substantial" enough to spend a model call on (TestCollector_NoNewSubstantialContentSkipsInvocation)
	// — a handful of one-line acks/no-ops should not each trigger a turn.
	MinNewContentBytes int
	// MaxPasses hard-caps how many turns ANY one watcher task can ever receive across its whole lifetime
	// (0 = unlimited — NOT the default; dev-plan.md's T5.3 "do not create an unbounded queue of processes"
	// reads as a hard requirement, so DefaultConfig always sets a finite cap).
	MaxPasses int
}

// DefaultConfig returns the Stage 5 placeholder defaults — see Config's doc comment on why these specific
// numbers and their "not yet tuned against a real stream" caveat.
func DefaultConfig() Config {
	return Config{
		Debounce:           3 * time.Second,
		Cooldown:           5 * time.Second,
		MaxBatch:           50,
		MinNewContentBytes: 200,
		MaxPasses:          20,
	}
}

// Filter decides whether one Observation is substantial enough to even be considered for a batch — e.g.
// dropping pure-noise tool calls. nil means "everything complete passes".
type Filter func(executor.Observation) bool

// Bundle is one fired batch: the observations it contains, the cursor position it advances the durable
// checkpoint store to (T5.2), and whether this is the watcher's FINAL pass (T5.4: MaxPasses reached — the
// caller must land this turn in a terminal status, never back in the resting "watching" state).
type Bundle struct {
	Observations []executor.Observation
	UpToSeq      int
	Final        bool
}

// Collector decides WHEN a watcher task should get its next turn, given a stream of Observations from the
// main stage agent it watches.
type Collector struct {
	cfg    Config
	clock  Clock
	filter Filter

	mu          sync.Mutex
	pending     []executor.Observation
	lastObsAt   time.Time
	busy        bool
	lastTurnEnd time.Time
	passes      int
	stopped     bool
}

// NewCollector builds a Collector. clock defaults to RealClock{} if nil; filter defaults to "every complete
// observation passes" if nil.
func NewCollector(cfg Config, clock Clock, filter Filter) *Collector {
	if clock == nil {
		clock = RealClock{}
	}
	return &Collector{cfg: cfg, clock: clock, filter: filter}
}

// Observe feeds one new Observation. Incomplete observations (Observation.Complete==false — a torn block, see
// pkg/executor/observation.go) are never checkpoint-eligible and are dropped here unconditionally, regardless
// of Filter.
func (c *Collector) Observe(o executor.Observation) {
	if !o.Complete {
		return
	}
	if c.filter != nil && !c.filter(o) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = append(c.pending, o)
	if c.cfg.MaxBatch > 0 && len(c.pending) > c.cfg.MaxBatch {
		// Keep the MOST RECENT MaxBatch observations — an unboundedly long quiet stage must never grow this
		// slice without bound (TestCheckpoint_StorageBounded's sibling guarantee at the collector level).
		c.pending = append([]executor.Observation{}, c.pending[len(c.pending)-c.cfg.MaxBatch:]...)
	}
	c.lastObsAt = c.clock.Now()
}

// SetBusy marks whether the watcher's own turn is currently in flight. While busy, Ready always reports false:
// new observations keep accumulating into the pending batch instead of spawning a second concurrent process —
// this coalescing is what keeps hundreds of main-agent events from spawning hundreds of watcher processes.
func (c *Collector) SetBusy(busy bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.busy = busy
	if !busy {
		c.lastTurnEnd = c.clock.Now()
	}
}

// Busy lets external loop restarts avoid competing with an already claimed checkpoint fire.
func (c *Collector) Busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.busy
}

// Stop permanently disables further firing (T5.4: TestTrigger_ParentFailedStopsNewPasses — the root stage
// failed; whatever the watcher already produced stays exactly as recorded, but no new pass will ever be
// requested again).
func (c *Collector) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
}

// Ready reports whether a new turn should be requested right now and, if so, the Bundle to hand it — content
// is popped from the internal pending buffer only when this returns true (that pop is what "commits" the
// coalescing decision; a caller that fails to actually start the turn must call Cancel to put it back).
func (c *Collector) Ready() (Bundle, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped || c.busy || len(c.pending) == 0 {
		return Bundle{}, false
	}
	now := c.clock.Now()
	if now.Sub(c.lastObsAt) < c.cfg.Debounce {
		return Bundle{}, false
	}
	if !c.lastTurnEnd.IsZero() && now.Sub(c.lastTurnEnd) < c.cfg.Cooldown {
		return Bundle{}, false
	}
	size := 0
	for _, o := range c.pending {
		size += len(o.Content)
	}
	if size < c.cfg.MinNewContentBytes {
		return Bundle{}, false
	}
	bundle := Bundle{
		Observations: c.pending,
		UpToSeq:      c.pending[len(c.pending)-1].Seq,
	}
	c.pending = nil
	c.passes++
	c.busy = true
	if c.cfg.MaxPasses > 0 && c.passes >= c.cfg.MaxPasses {
		bundle.Final = true
		c.stopped = true
	}
	return bundle, true
}

// Cancel returns a bundle Ready() produced back to pending WITHOUT counting it as a real pass — used when the
// caller could not actually durably commit the checkpoint (TestCheckpoint_MissingLogMeansNotVerified's sibling
// guarantee: a storage failure must not silently "spend" a pass nor lose the content).
func (c *Collector) Cancel(b Bundle) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = append(b.Observations, c.pending...)
	c.passes--
	c.busy = false
	if b.Final {
		c.stopped = false
	}
}

// Passes reports how many turns have been fired so far — exposed for tests and for UI display (T5.6's "pass
// count").
func (c *Collector) Passes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.passes
}

// ForceFinal requests that the NEXT Ready() (or, if one is already pending, the CURRENT in-flight bundle's
// caller) treat this as the watcher's last pass — used by T5.4's on_completion/required-final-pass rule, which
// needs a bounded last word independent of Debounce/Cooldown/MinNewContentBytes. If nothing is pending and the
// collector is already idle, the caller is expected to still fire a turn directly (an empty "no new content"
// final pass is legitimate: a required watcher must get a chance to speak even if the main agent produced
// nothing new since its last checkpoint).
func (c *Collector) ForceFinal() Bundle {
	c.mu.Lock()
	defer c.mu.Unlock()
	bundle := Bundle{Observations: c.pending, UpToSeq: 0, Final: true}
	if len(c.pending) > 0 {
		bundle.UpToSeq = c.pending[len(c.pending)-1].Seq
	}
	c.pending = nil
	c.passes++
	c.busy = true
	c.stopped = true
	return bundle
}
