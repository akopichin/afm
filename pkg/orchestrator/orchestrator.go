package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/state"
)

// phasePlanning is the agent phase name used for planning agents.
const phasePlanning = "planning"

const phaseImplementation = "implementation"
const phaseReview = "review"

// semNop is a no-op semaphore used when MaxParallel is 0 (unlimited).
type semNop struct{}

func (semNop) acquire() {}
func (semNop) release() {}

// semChan is a real semaphore backed by a buffered channel.
type semChan chan struct{}

func (s semChan) acquire() { s <- struct{}{} }
func (s semChan) release() { <-s }

// Prompts holds the prompt templates for each agent type.
type Prompts struct {
	Planning       string
	Implementation string
	Review         string
	Summary        string
}

// DefaultPrompts returns empty prompts (will be set from assets).
func DefaultPrompts() Prompts { return Prompts{} }

// Options configures an Orchestrator.
type Options struct {
	RunDir       string
	Stages       []flow.Stage
	State        *state.RunState
	StateFile    string
	Config       config.Config
	Prompts      Prompts
	Runner       executor.Runner // nil = real Executor
	DashboardURL string          // e.g. "http://127.0.0.1:9876"
}

// Orchestrator manages the full lifecycle of a flow run via event loop.
type Orchestrator struct {
	opts   Options
	graph  *Graph
	runner executor.Runner
	bus    *EventBus
	sems   map[string]interface {
		acquire()
		release()
	} // per-command semaphores
	mu sync.Mutex
}

// New creates an Orchestrator.
func New(opts Options) *Orchestrator {
	bus := NewEventBus()

	r := opts.Runner
	if r == nil {
		r = executor.New(executor.Config{
			Command:     opts.Config.Client.Command,
			ExtraArgs:   opts.Config.Client.ExtraArgs,
			IdleTimeout: opts.Config.Executor.IdleTimeout,
			OnAction:    actionPublisher(bus, ""),
		})
	}

	// Build per-command semaphores from stage configs.
	sems := make(map[string]interface {
		acquire()
		release()
	})
	globalMP := opts.Config.Executor.MaxParallel
	for _, s := range opts.Stages {
		cmd := s.Command
		if cmd == "" {
			cmd = opts.Config.Client.Command
		}
		if _, exists := sems[cmd]; exists {
			continue
		}
		mp := s.MaxParallel
		if mp <= 0 {
			mp = globalMP
		}
		if mp > 0 {
			sems[cmd] = semChan(make(chan struct{}, mp))
		} else {
			sems[cmd] = semNop{}
		}
	}

	return &Orchestrator{
		opts:   opts,
		graph:  NewGraph(opts.Stages),
		runner: r,
		bus:    bus,
		sems:   sems,
	}
}

// Bus returns the EventBus for external subscribers (server, WebSocket).
func (o *Orchestrator) Bus() *EventBus { return o.bus }

// SetDashboardURL sets the dashboard URL after the server starts listening.
func (o *Orchestrator) SetDashboardURL(url string) { o.opts.DashboardURL = url }

// FailStage marks a stage as failed with a reason.
func (o *Orchestrator) FailStage(stageID, reason string) {
	o.setStatus(stageID, state.StatusFailed)
	o.failBlockedStages()
}

// runnerFor returns the appropriate Runner for a stage's phase.
// For interactive stages, generates mcp.json and a session id, then
// returns an executor configured with --mcp-config and --session-id (or --resume).
func (o *Orchestrator) runnerFor(s flow.Stage, phase string) executor.Runner {
	if !s.Interactive {
		if s.Command == "" {
			return o.runner
		}
		return executor.New(executor.Config{
			Command:     s.Command,
			IdleTimeout: o.opts.Config.Executor.IdleTimeout,
			OnAction:    actionPublisher(o.bus, s.ID),
		})
	}

	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	mcpPath, err := writeMcpConfig(stageDir, s.ID, phase, o.opts.DashboardURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: interactive stage %q: mcp config failed: %v; using non-interactive runner\n", s.ID, err)
		return o.runnerForFallback(s)
	}

	resume := sessionExists(stageDir, phase)
	sessionID, err := loadOrCreateSession(stageDir, phase)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: interactive stage %q: session failed: %v; using non-interactive runner\n", s.ID, err)
		return o.runnerForFallback(s)
	}

	cmd := s.Command
	if cmd == "" {
		cmd = o.opts.Config.Client.Command
	}
	requiredArgs := []string{"--print", "--output-format", "stream-json", "--dangerously-skip-permissions"}
	extraArgs := append(requiredArgs, o.opts.Config.Client.ExtraArgs...)
	return executor.New(executor.Config{
		Command:     cmd,
		ExtraArgs:   extraArgs,
		IdleTimeout: o.opts.Config.Executor.IdleTimeout,
		OnAction:    actionPublisher(o.bus, s.ID),
		SessionID:   sessionID,
		Resume:      resume,
		McpConfig:   mcpPath,
	})
}

func (o *Orchestrator) runnerForFallback(s flow.Stage) executor.Runner {
	if s.Command == "" {
		return o.runner
	}
	return executor.New(executor.Config{
		Command:     s.Command,
		IdleTimeout: o.opts.Config.Executor.IdleTimeout,
		OnAction:    actionPublisher(o.bus, s.ID),
	})
}

func actionPublisher(bus *EventBus, stageID string) func(string, string) {
	return func(tool, detail string) {
		bus.Publish(Event{Type: EventAgentAction, StageID: stageID, Data: map[string]string{
			"tool":   tool,
			"detail": detail,
		}})
	}
}

// semFor returns the semaphore for a stage's effective command.
func (o *Orchestrator) semFor(s flow.Stage) interface {
	acquire()
	release()
} {
	cmd := s.Command
	if cmd == "" {
		cmd = o.opts.Config.Client.Command
	}
	if sem, ok := o.sems[cmd]; ok {
		return sem
	}
	return semNop{}
}

// Run starts the event-driven orchestrator loop.
func (o *Orchestrator) Run(ctx context.Context) error {
	events := o.bus.Subscribe()
	defer o.bus.Unsubscribe(events)

	o.startPlanningForPending(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if err := o.handleEvent(ctx, ev); err != nil {
				return err
			}
			if o.allTerminal() {
				return nil
			}
		}
	}
}

// Approve approves a stage plan.
func (o *Orchestrator) Approve(stageID string) {
	o.bus.Publish(Event{Type: EventApproved, StageID: stageID})
}

// Revise sends feedback to re-plan a stage.
func (o *Orchestrator) Revise(stageID, feedback string) {
	o.bus.Publish(Event{Type: EventRevised, StageID: stageID, Data: feedback})
}

// Retry retries a failed stage by transitioning it to pending and restarting.
func (o *Orchestrator) Retry(stageID string) {
	o.bus.Publish(Event{Type: EventManualRetry, StageID: stageID})
}

// handleEvent dispatches events to the appropriate handler.
func (o *Orchestrator) handleEvent(ctx context.Context, ev Event) error {
	switch ev.Type {
	case EventAgentCompleted:
		return o.onAgentCompleted(ctx, ev)
	case EventApproved:
		return o.onApproved(ctx, ev)
	case EventRevised:
		return o.onRevised(ctx, ev)
	case EventManualRetry:
		return o.onManualRetry(ctx, ev)
	case EventUserAnswered:
		return o.onUserAnswered(ctx, ev)
	}
	return nil
}

func (o *Orchestrator) onAgentCompleted(ctx context.Context, ev Event) error {
	agentType, _ := ev.Data.(string)
	current := o.currentStatus(ev.StageID)

	// Open-question gate: if the agent finished but the user has not yet
	// answered an ask_user question, hold the stage in awaiting_user_input.
	// The stage resumes on EventUserAnswered.
	if o.hasOpenQuestion(ev.StageID, agentType) {
		o.setStatus(ev.StageID, state.StatusAwaitingUserInput)
		return nil
	}

	switch agentType {
	case phasePlanning:
		// Ignore stale completion if stage already left planning state
		// (e.g. approved, done, or restarted by onUserAnswered).
		if current != state.StatusPlanning && current != state.StatusRetrying {
			return nil
		}
		o.setStatus(ev.StageID, state.StatusAwaitingApproval)
		o.tryActivatePrePlanned(ctx)
	case phaseImplementation:
		if current != state.StatusRunning && current != state.StatusRetrying {
			return nil
		}
		o.setStatus(ev.StageID, state.StatusDone)
		o.failBlockedStages()
		o.startReadyStages(ctx)
		o.tryActivatePrePlanned(ctx)
	default:
		// review or unknown agent type: no status change needed
	}
	return nil
}

// hasOpenQuestion reports whether the dialog file for the given stage/phase
// contains an ask_user question that has not yet been answered.
func (o *Orchestrator) hasOpenQuestion(stageID, phase string) bool {
	if phase == "" {
		return false
	}
	dialogPath := filepath.Join(o.opts.RunDir, stageID, phase+".dialog.jsonl")
	open, err := mcp.HasOpenQuestions(dialogPath)
	if err != nil {
		return false
	}
	return open
}

// onUserAnswered resumes a stage that was paused on awaiting_user_input
// once the user's answer has been recorded. If a waiter inside the MCP
// server already delivered the answer to a still-running agent, the
// status will not be awaiting_user_input here and this is a no-op.
func (o *Orchestrator) onUserAnswered(ctx context.Context, ev Event) error {
	if o.currentStatus(ev.StageID) != state.StatusAwaitingUserInput {
		return nil
	}

	data, _ := ev.Data.(map[string]any)
	phase, _ := data["phase"].(string)
	if phase == "" {
		return nil
	}

	if o.hasOpenQuestion(ev.StageID, phase) {
		return nil
	}

	stage := o.graph.Stage(ev.StageID)
	if stage == nil {
		return nil
	}

	switch phase {
	case phasePlanning:
		o.setStatus(ev.StageID, state.StatusPlanning)
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			defer sem.release()
			o.runPlanningAgent(ctx, st)
		}(*stage)
	default:
		o.setStatus(ev.StageID, state.StatusRunning)
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			defer sem.release()
			o.runImplementationAgent(ctx, st)
		}(*stage)
	}
	return nil
}

func (o *Orchestrator) onApproved(ctx context.Context, ev Event) error {
	if o.currentStatus(ev.StageID) != state.StatusAwaitingApproval {
		return nil
	}
	stage := o.graph.Stage(ev.StageID)
	if stage != nil && !stage.HasAgent(flow.AgentImplementation) {
		// Planning-only stage — nothing to implement, mark as done.
		o.setStatus(ev.StageID, state.StatusDone)
		o.startReadyStages(ctx)
		o.tryActivatePrePlanned(ctx)
		return nil
	}
	o.setStatus(ev.StageID, state.StatusReady)
	o.startReadyStages(ctx)
	o.tryActivatePrePlanned(ctx)
	return nil
}

func (o *Orchestrator) onRevised(ctx context.Context, ev Event) error {
	stageID := ev.StageID

	if o.currentStatus(stageID) != state.StatusAwaitingApproval {
		return nil
	}

	feedback, _ := ev.Data.(string)

	o.setStatus(stageID, state.StatusRevising)

	stageDir := filepath.Join(o.opts.RunDir, stageID)
	if _, err := state.VersionPlan(stageDir); err != nil {
		return fmt.Errorf("version plan for %s: %w", stageID, err)
	}
	if err := state.SaveFeedback(stageDir, feedback); err != nil {
		return fmt.Errorf("save feedback for %s: %w", stageID, err)
	}

	o.setStatus(stageID, state.StatusPlanning)

	stage := o.graph.Stage(stageID)
	if stage != nil {
		s := *stage
		sem := o.semFor(s)
		go func() {
			sem.acquire()
			defer sem.release()
			o.runPlanningWithFeedback(ctx, s)
		}()
	}
	return nil
}

func (o *Orchestrator) onManualRetry(ctx context.Context, ev Event) error {
	stageID := ev.StageID

	current := o.currentStatus(stageID)

	if current != state.StatusFailed {
		return nil
	}

	stage := o.graph.Stage(stageID)
	if stage == nil {
		return nil
	}

	o.setStatus(stageID, state.StatusPending)

	if !stage.NeedsPlanning() {
		o.setStatus(stageID, state.StatusReady)
		o.setStatus(stageID, state.StatusRunning)
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			defer sem.release()
			o.runImplementationAgent(ctx, st)
		}(*stage)
		o.startReadyStages(ctx)
		return nil
	}

	stageDir := filepath.Join(o.opts.RunDir, stageID)
	planPath := filepath.Join(stageDir, "plan.md")
	if _, err := os.Stat(planPath); err == nil {
		o.setStatus(stageID, state.StatusReady)
		o.setStatus(stageID, state.StatusRunning)
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			defer sem.release()
			o.runImplementationAgent(ctx, st)
		}(*stage)
	} else {
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			defer sem.release()
			o.runPlanningAgent(ctx, st)
		}(*stage)
	}

	return nil
}

// startPlanningForPending starts or resumes stages based on their saved status.
// Terminal states (done, failed, awaiting_approval) are left untouched.
// Interrupted transient states (planning, running, revising) are restarted.
// Pending stages start planning for the first time.
func (o *Orchestrator) startPlanningForPending(ctx context.Context) {
	for _, s := range o.opts.Stages {
		if !s.NeedsPlanning() {
			o.mu.Lock()
			current := o.opts.State.Stages[s.ID].Status
			o.mu.Unlock()

			switch current {
			case state.StatusDone, state.StatusFailed, state.StatusAwaitingApproval:
				continue
			case state.StatusRunning, state.StatusReady, state.StatusAwaitingUserInput:
				// Let these fall through to the normal resume logic below.
			case state.StatusRetrying:
				// Interrupted retry for pre-planned stage — check .done or restart implementation
				stageDir := filepath.Join(o.opts.RunDir, s.ID)
				if err := checkCompletion(stageDir, ".", s); err == nil {
					o.setStatus(s.ID, state.StatusDone)
					continue
				}
				o.setStatus(s.ID, state.StatusReady)
			default:
				// Pending or other — activate if deps are done.
				if !o.depsDone(s) {
					continue
				}

				stageDir := filepath.Join(o.opts.RunDir, s.ID)
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					o.setStatus(s.ID, state.StatusFailed)
					continue
				}
				dst := filepath.Join(stageDir, "plan.md")
				if s.Plan != "" {
					if err := copyFile(s.Plan, dst); err != nil {
						o.setStatus(s.ID, state.StatusFailed)
						continue
					}
				} else if s.Interactive {
					if err := os.WriteFile(dst, []byte(s.Description), 0644); err != nil {
						o.setStatus(s.ID, state.StatusFailed)
						continue
					}
				}
				o.setStatus(s.ID, state.StatusReady)
				continue
			}
		}

		o.mu.Lock()
		current := o.opts.State.Stages[s.ID].Status
		o.mu.Unlock()

		switch current {
		case state.StatusDone, state.StatusFailed, state.StatusAwaitingApproval, state.StatusReady:
			continue
		case state.StatusAwaitingUserInput:
			go func(st flow.Stage) {
				sem := o.semFor(st)
				sem.acquire()
				defer sem.release()
				o.resumeInteractiveAgent(ctx, st)
			}(s)
		case state.StatusRetrying:
			// Interrupted retry — check completion or restart
			stageDir := filepath.Join(o.opts.RunDir, s.ID)
			if err := checkCompletion(stageDir, ".", s); err == nil {
				o.setStatus(s.ID, state.StatusDone)
				continue
			}
			if checkPlanCompletion(stageDir) == nil && s.NeedsPlanning() {
				o.setStatus(s.ID, state.StatusAwaitingApproval)
				continue
			}
			// Restart planning from scratch
			o.setStatus(s.ID, state.StatusPlanning)
			go func(stage flow.Stage) {
				sem := o.semFor(stage)
				sem.acquire()
				defer sem.release()
				o.runPlanningAgent(ctx, stage)
			}(s)
		case state.StatusRevising:
			// Interrupted revision — restart with feedback
			go func() {
				sem := o.semFor(s)
				sem.acquire()
				defer sem.release()
				o.runPlanningWithFeedback(ctx, s)
			}()
		case state.StatusRunning:
			// Check if .done exists (agent completed but orchestrator missed the event)
			stageDir := filepath.Join(o.opts.RunDir, s.ID)
			if err := checkCompletion(stageDir, ".", s); err == nil {
				o.setStatus(s.ID, state.StatusDone)
				continue
			}
			// Interrupted implementation — restart with existing plan
			go func(st flow.Stage) {
				sem := o.semFor(st)
				sem.acquire()
				defer sem.release()
				o.runImplementationAgent(ctx, st)
			}(s)
		default:
			// Pending, planning, or unknown — check if planning already completed
			if s.NeedsPlanning() {
				stageDir := filepath.Join(o.opts.RunDir, s.ID)
				if checkPlanCompletion(stageDir) == nil {
					o.setStatus(s.ID, state.StatusAwaitingApproval)
					continue
				}
			}
			// (Re)start planning (planning runs eagerly before deps are done)
			o.setStatus(s.ID, state.StatusPlanning)
			go func(stage flow.Stage) {
				sem := o.semFor(stage)
				sem.acquire()
				defer sem.release()
				o.runPlanningAgent(ctx, stage)
			}(s)
		}
	}

	// Cascade failures to stages blocked by failed dependencies.
	o.failBlockedStages()

	// Start implementation for stages that are ready.
	o.startReadyStages(ctx)

	// Activate pre-planned stages whose deps just became satisfied.
	o.tryActivatePrePlanned(ctx)
}

// resumeInteractiveAgent re-runs the agent of the phase whose
// session.json exists most recently. The phase is detected by looking at
// mtimes of <phase>.session.json files in the stage directory.
func (o *Orchestrator) resumeInteractiveAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	phase := o.detectInterruptedPhase(stageDir)

	switch phase {
	case phasePlanning:
		o.setStatus(s.ID, state.StatusPlanning)
		o.runPlanningAgent(ctx, s)
	case phaseReview:
		// Review runs after implementation completes; if we see review session
		// open, the stage was paused inside review. Fall through to implementation
		// agent which will re-trigger review at the end.
		fallthrough
	default:
		o.setStatus(s.ID, state.StatusRunning)
		o.runImplementationAgent(ctx, s)
	}
}

func (o *Orchestrator) detectInterruptedPhase(stageDir string) string {
	var latestPhase string
	var latestMtime time.Time
	for _, phase := range []string{phasePlanning, phaseImplementation, phaseReview} {
		fi, err := os.Stat(sessionFile(stageDir, phase))
		if err != nil {
			continue
		}
		if fi.ModTime().After(latestMtime) {
			latestMtime = fi.ModTime()
			latestPhase = phase
		}
	}
	return latestPhase
}

// depsDone checks whether all dependencies of a stage are in StatusDone.
func (o *Orchestrator) depsDone(s flow.Stage) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, dep := range s.DependsOn {
		st, ok := o.opts.State.Stages[dep]
		if !ok || st.Status != state.StatusDone {
			return false
		}
	}
	return true
}

// tryActivatePrePlanned checks all pre-planned stages (those with Plan != "")
// and activates any whose dependencies are now done but status is still pending.
func (o *Orchestrator) tryActivatePrePlanned(ctx context.Context) {
	for _, s := range o.opts.Stages {
		if s.NeedsPlanning() {
			continue
		}

		o.mu.Lock()
		current := o.opts.State.Stages[s.ID].Status
		o.mu.Unlock()

		if current != state.StatusPending {
			continue
		}

		if !o.depsDone(s) {
			continue
		}

		stageDir := filepath.Join(o.opts.RunDir, s.ID)
		if err := os.MkdirAll(stageDir, 0755); err != nil {
			o.setStatus(s.ID, state.StatusFailed)
			continue
		}
		dst := filepath.Join(stageDir, "plan.md")
		if err := copyFile(s.Plan, dst); err != nil {
			o.setStatus(s.ID, state.StatusFailed)
			continue
		}
		o.setStatus(s.ID, state.StatusReady)
	}

	// Newly activated stages may now be ready to run.
	o.startReadyStages(ctx)
}

// startReadyStages starts implementation for stages whose dependencies are done.
func (o *Orchestrator) startReadyStages(ctx context.Context) {
	o.mu.Lock()
	statuses := make(map[string]state.StageStatus, len(o.opts.State.Stages))
	for id, s := range o.opts.State.Stages {
		statuses[id] = s.Status
	}
	o.mu.Unlock()

	ready := o.graph.ReadyStages(statuses)
	for _, id := range ready {
		stage := o.graph.Stage(id)
		if stage == nil {
			continue
		}
		o.setStatus(id, state.StatusRunning)
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			defer sem.release()
			o.runImplementationAgent(ctx, st)
		}(*stage)
	}
}

func (o *Orchestrator) runPlanningAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.setStatus(s.ID, state.StatusFailed)
		return
	}

	o.setStatus(s.ID, state.StatusPlanning)

	o.runWithRetry(ctx, s, phasePlanning, func(retryContext string) error {
		depCtx := o.buildStageContext(s)
		prompt := buildPlanningPrompt(o.opts.Prompts.Planning, s, depCtx+retryContext)
		outFile := filepath.Join(stageDir, "plan.md")
		logFile := filepath.Join(stageDir, "planning.log")

		r := o.runnerFor(s, phasePlanning)
		return r.RunPlanning(ctx, s.Name, prompt, outFile, logFile)
	}, func() error {
		return checkPlanCompletion(stageDir)
	})
}

func (o *Orchestrator) runPlanningWithFeedback(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	o.setStatus(s.ID, state.StatusPlanning)

	o.runWithRetry(ctx, s, phasePlanning, func(retryContext string) error {
		feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
		var prevPlan string
		planVersionRe := regexp.MustCompile(`^plan\.v(\d+)\.md$`)
		var bestVer int
		entries, _ := os.ReadDir(stageDir)
		for _, e := range entries {
			m := planVersionRe.FindStringSubmatch(e.Name())
			if m == nil {
				continue
			}
			v, _ := strconv.Atoi(m[1])
			if v > bestVer {
				bestVer = v
				data, _ := os.ReadFile(filepath.Join(stageDir, e.Name()))
				prevPlan = string(data)
			}
		}

		depCtx := o.buildStageContext(s)
		prompt := buildRevisionPrompt(o.opts.Prompts.Planning, s, prevPlan, string(feedbackData), depCtx+retryContext)
		outFile := filepath.Join(stageDir, "plan.md")
		logFile := filepath.Join(stageDir, "planning-revision.log")

		r := o.runnerFor(s, phasePlanning)
		return r.RunPlanning(ctx, s.Name, prompt, outFile, logFile)
	}, func() error {
		return checkPlanCompletion(stageDir)
	})
}

func (o *Orchestrator) runImplementationAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	o.runWithRetry(ctx, s, phaseImplementation, func(retryContext string) error {
		planData, err := os.ReadFile(filepath.Join(stageDir, "plan.md"))
		if err != nil {
			return err
		}

		depCtx := o.buildStageContext(s)
		prompt := buildImplementationPrompt(o.opts.Prompts.Implementation, s, string(planData), depCtx+retryContext, stageDir)
		logFile := filepath.Join(stageDir, "implementation.log")

		r := o.runnerFor(s, phaseImplementation)
		if err := r.RunAgent(ctx, string(s.ImplAgent()), s.Name, prompt, logFile); err != nil {
			return err
		}

		if s.HasAgent(flow.AgentReview) {
			reviewPrompt := buildReviewPrompt(o.opts.Prompts.Review, s)
			reviewLog := filepath.Join(stageDir, "review.log")
			rr := o.runnerFor(s, phaseReview)
			if err := rr.RunAgent(ctx, phaseReview, s.Name, reviewPrompt, reviewLog); err != nil {
				return err
			}
		}

		return nil
	}, func() error {
		return checkCompletion(stageDir, ".", s)
	})
}

// failBlockedStages marks pending stages as failed if any of their
// dependencies are in StatusFailed. This prevents the flow from hanging
// when a dependency fails and dependent stages can never start.
func (o *Orchestrator) failBlockedStages() {
	changed := true
	for changed {
		changed = false
		for _, s := range o.opts.Stages {
			o.mu.Lock()
			current := o.opts.State.Stages[s.ID].Status
			o.mu.Unlock()

			if current != state.StatusPending {
				continue
			}

			for _, dep := range s.DependsOn {
				o.mu.Lock()
				depState := o.opts.State.Stages[dep]
				o.mu.Unlock()

				if depState.Status == state.StatusFailed {
					o.setStatus(s.ID, state.StatusFailed)
					changed = true
					break
				}
			}
		}
	}
}

func (o *Orchestrator) allTerminal() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.opts.State.Stages) == 0 {
		return true
	}
	for _, s := range o.opts.State.Stages {
		if !IsTerminal(s.Status) {
			return false
		}
	}
	return true
}

func (o *Orchestrator) currentStatus(id string) state.StageStatus {
	o.mu.Lock()
	defer o.mu.Unlock()
	if s, ok := o.opts.State.Stages[id]; ok {
		return s.Status
	}
	return state.StatusPending
}

func (o *Orchestrator) setStatus(id string, status state.StageStatus) {
	o.mu.Lock()
	o.opts.State.SetStageStatus(id, status)
	if err := o.opts.State.Save(o.opts.StateFile); err != nil {
		log.Printf("CRITICAL: failed to save state after setting %s to %s: %v", id, status, err)
	}
	o.mu.Unlock()

	o.bus.Publish(Event{Type: EventStageStatusChanged, StageID: id, Data: string(status)})
}

// buildStageContext collects dependency plans and artifact contents for a stage's prompt.
func (o *Orchestrator) buildStageContext(s flow.Stage) string {
	plans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages)
	artifacts, _ := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
	return plans + artifacts
}

func buildPlanningPrompt(template string, s flow.Stage, dependencyContext string) string {
	extra := ""
	if len(s.Skills) > 0 {
		extra = "\n\nUse these skills: " + joinStrings(s.Skills)
	}
	interactive := ""
	if s.Interactive {
		// Override the "do NOT ask questions" rule for interactive stages
		template = interactivePlanningOverride + template
		interactive = "\n\nThis stage is interactive. You can use the `ask_user` MCP tool to ask the user questions before finalizing the plan. Each question can include options for the user to choose from. Ask one question at a time."
	}
	return fmt.Sprintf("%s\n\n## Stage: %s\n\n%s%s%s%s", template, s.Name, s.Description, extra, interactive, dependencyContext)
}

const interactivePlanningOverride = `IMPORTANT: This is an interactive stage. The rules below that say "Do NOT ask questions" are OVERRIDDEN — you SHOULD ask the user questions using the ` + "`ask_user`" + ` MCP tool when you need clarification or decisions.

Rules for ask_user:
- Ask ONE question at a time using the mcp__flowmanager__ask_user tool.
- The tool BLOCKS until the user answers — this is expected behavior. Do NOT cancel, retry, or move on.
- If the tool appears to time out or return an error, WAIT and try calling it again with the SAME id. Do NOT proceed without the answer.
- Never say "интерактивный инструмент не отвечает" or switch to autonomous mode. ALWAYS wait for the user's response.
- After receiving the answer, incorporate it into your plan and continue.

`

func buildRevisionPrompt(template string, s flow.Stage, prevPlan, feedback, dependencyContext string) string {
	extra := ""
	if len(s.Skills) > 0 {
		extra = "\n\nUse these skills: " + joinStrings(s.Skills)
	}
	return fmt.Sprintf(
		"%s\n\n## Stage: %s\n\n%s%s%s\n\n## Previous plan (needs revision)\n\n%s\n\n## Feedback\n\n%s\n\nRevise the plan according to the feedback above.",
		template, s.Name, s.Description, extra, dependencyContext, prevPlan, feedback,
	)
}

func buildImplementationPrompt(template string, s flow.Stage, plan, dependencyContext, stageDir string) string {
	extra := ""
	if len(s.Skills) > 0 {
		extra = "\n\nUse these skills: " + joinStrings(s.Skills)
	}
	desc := ""
	if s.Description != "" {
		desc = "\n\n## Instructions\n\n" + s.Description
	}
	interactive := ""
	if s.Interactive {
		interactive = "\n\nThis stage is interactive. You can use the `ask_user` MCP tool to ask the user questions during implementation. Each question can include options for the user to choose from."
	}
	artifacts := buildArtifactInstructions(s, stageDir)
	doneInstr := fmt.Sprintf("\n\nStage directory for .done file: %s", stageDir)
	return fmt.Sprintf("%s\n\n## Stage: %s\n%s\n\n## Plan\n\n%s%s%s%s%s%s", template, s.Name, dependencyContext, plan, desc, extra, interactive, artifacts, doneInstr)
}

// buildArtifactInstructions returns a prompt section listing each declared
// artifact with its fully resolved path. Paths starting with "./" resolve
// to the stage run directory (orchestrator convention) — agents have no way
// to know that without being told, so the resolved path is shown explicitly.
func buildArtifactInstructions(s flow.Stage, stageDir string) string {
	if len(s.Artifacts) == 0 {
		return ""
	}
	var buf strings.Builder
	buf.WriteString("\n\n## Required Artifacts\n\n")
	buf.WriteString("Each artifact below MUST exist at the EXACT path shown when this stage finishes. ")
	buf.WriteString("The stage will be marked failed if any path is missing, even if all plan tasks are done.\n\n")
	for _, art := range s.Artifacts {
		dst := art.Path
		if strings.HasPrefix(art.Path, "./") {
			dst = filepath.Join(stageDir, art.Path[2:])
		}
		desc := ""
		if art.Description != "" {
			desc = " — " + art.Description
		}
		fmt.Fprintf(&buf, "- `%s`%s\n  Write to: `%s`\n", art.Name, desc, dst)
	}
	return buf.String()
}

func buildReviewPrompt(template string, s flow.Stage) string {
	return fmt.Sprintf("%s\n\n## Stage: %s\n\n%s", template, s.Name, s.Description)
}

// CollectDependencyPlans reads plan.md from each stage in DependsOn
// and returns a formatted prompt section. Missing plans produce a warning comment.
func CollectDependencyPlans(runDir string, stage flow.Stage, allStages []flow.Stage) string {
	if len(stage.DependsOn) == 0 {
		return ""
	}

	nameIndex := make(map[string]string, len(allStages))
	for _, s := range allStages {
		nameIndex[s.ID] = s.Name
	}

	var buf strings.Builder
	buf.WriteString("\n\n## Context from dependent stages\n")

	for _, depID := range stage.DependsOn {
		planPath := filepath.Join(runDir, depID, "plan.md")
		data, err := os.ReadFile(planPath)
		name := nameIndex[depID]
		if name == "" {
			name = depID
		}
		fmt.Fprintf(&buf, "\n### Stage: %s (%s)\n\n", name, depID)
		if err != nil {
			buf.WriteString("(plan not available)\n")
			continue
		}
		buf.WriteString(string(data))
		buf.WriteString("\n")
	}

	return buf.String()
}

// resolveArtifactPath resolves an artifact path to an absolute file path.
// Paths starting with "./" are relative to the stage's run directory.
// All other paths are relative to the project directory.
func resolveArtifactPath(projectDir, runDir, stageID, artifactPath string) string {
	if strings.HasPrefix(artifactPath, "./") {
		return filepath.Join(runDir, stageID, artifactPath[2:])
	}
	return filepath.Join(projectDir, artifactPath)
}

// CollectArtifacts reads artifact files referenced by a stage's Inputs
// and returns a formatted prompt section. Returns an error if a required
// artifact file is missing.
func CollectArtifacts(projectDir, runDir string, stage flow.Stage, allStages []flow.Stage) (string, error) {
	if len(stage.Inputs) == 0 {
		return "", nil
	}

	// Build index: stageID -> artifactName -> Artifact
	artIndex := make(map[string]map[string]flow.Artifact, len(allStages))
	for _, s := range allStages {
		m := make(map[string]flow.Artifact, len(s.Artifacts))
		for _, a := range s.Artifacts {
			m[a.Name] = a
		}
		artIndex[s.ID] = m
	}

	var buf strings.Builder
	buf.WriteString("\n\n## Artifacts\n")
	hasContent := false

	for _, inp := range stage.Inputs {
		parts := strings.SplitN(inp.Ref, ".", 2)
		stageID, artName := parts[0], parts[1]
		art := artIndex[stageID][artName]

		resolved := resolveArtifactPath(projectDir, runDir, stageID, art.Path)

		if art.IsInline() {
			data, err := os.ReadFile(resolved)
			if err != nil {
				if inp.Optional {
					continue
				}
				return "", fmt.Errorf("required artifact %q (stage %q): %w", artName, stageID, err)
			}
			fmt.Fprintf(&buf, "\n### %s (from %s): %s\n\n", artName, stageID, art.Description)
			buf.Write(data)
			buf.WriteString("\n")
			hasContent = true
		} else {
			if _, err := os.Stat(resolved); err != nil {
				if inp.Optional {
					continue
				}
				return "", fmt.Errorf("required artifact %q (stage %q): %w", artName, stageID, err)
			}
			fmt.Fprintf(&buf, "\n### %s (from %s): %s\n\nFile path: %s\n(Use Read tool to access this file)\n", artName, stageID, art.Description, resolved)
			hasContent = true
		}
	}

	if !hasContent {
		return "", nil
	}
	return buf.String(), nil
}

func joinStrings(ss []string) string {
	return strings.Join(ss, ", ")
}

const (
	retryTooMany             = "too many requests"
	retryOverloaded          = "overloaded"
	retryCapacity            = "capacity"
	retryInternalServerError = "internal server error"
)

// isRetryableError checks if the error is a rate limit or server error (retryable with backoff).
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	retryPatterns := []string{
		"hit your limit",
		"rate limit",
		retryTooMany,
		retryOverloaded,
		retryCapacity,
		"http 500",
		"status 500",
		retryInternalServerError,
	}
	for _, p := range retryPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// buildRetryContext reads the last N lines from the agent log file
// and formats them as a continuation context for the retry prompt.
func buildRetryContext(stageDir, phase string) string {
	var logName string
	switch phase {
	case phasePlanning:
		logName = "planning.log"
	default:
		logName = "implementation.log"
	}

	data, err := os.ReadFile(filepath.Join(stageDir, logName))
	if err != nil {
		return ""
	}

	lines := strings.Split(string(data), "\n")
	const maxLines = 200
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}

	var buf strings.Builder
	buf.WriteString("\n\n## Previously completed actions (resuming after interruption)\n\n")
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			buf.WriteString(l)
			buf.WriteString("\n")
		}
	}
	buf.WriteString("\nContinue from where you left off. Do NOT redo work that is already done.\n")
	return buf.String()
}

// RetryBackoff defines wait durations between retry attempts.
var RetryBackoff = []time.Duration{5 * time.Second, 10 * time.Second, 30 * time.Second}

// runWithRetry wraps an agent function with automatic retry on rate limit errors.
// On rate limit: sets status to retrying, waits with backoff, then retries.
// After exhausting all retries: publishes EventRetryExhausted.
func (o *Orchestrator) runWithRetry(ctx context.Context, s flow.Stage, phase string, agentFn func(retryContext string) error, completionCheck func() error) {
	backoff := append([]time.Duration{}, RetryBackoff...)
	for attempt := 0; attempt <= len(backoff); attempt++ {
		retryCtx := ""
		if attempt > 0 {
			stageDir := filepath.Join(o.opts.RunDir, s.ID)
			retryCtx = buildRetryContext(stageDir, phase)
		}

		err := agentFn(retryCtx)
		if err == nil {
			if completionCheck == nil {
				o.bus.Publish(Event{Type: EventAgentCompleted, StageID: s.ID, Data: phase})
				return
			}
			checkErr := completionCheck()
			if checkErr == nil {
				o.bus.Publish(Event{Type: EventAgentCompleted, StageID: s.ID, Data: phase})
				return
			}
			// Incomplete work — retry once without backoff
			if isIncompleteWorkError(checkErr) && attempt == 0 {
				o.bus.Publish(Event{
					Type:    EventStageStatusChanged,
					StageID: s.ID,
					Data:    "incomplete work, retrying: " + checkErr.Error(),
				})
				continue
			}
			// Missing artifact or second incomplete attempt — fail
			o.setStatus(s.ID, state.StatusFailed)
			o.failBlockedStages()
			return
		}

		if !isRetryableError(err) {
			o.setStatus(s.ID, state.StatusFailed)
			o.failBlockedStages()
			return
		}

		if attempt < len(backoff) {
			o.setStatus(s.ID, state.StatusRetrying)
			o.bus.Publish(Event{
				Type:    EventRetryScheduled,
				StageID: s.ID,
				Data:    fmt.Sprintf("attempt %d/%d in %v", attempt+1, len(backoff), backoff[attempt]),
			})
			select {
			case <-time.After(backoff[attempt]):
			case <-ctx.Done():
				o.setStatus(s.ID, state.StatusFailed)
				o.failBlockedStages()
				return
			}
			switch phase {
			case phasePlanning:
				o.setStatus(s.ID, state.StatusPlanning)
			default:
				o.setStatus(s.ID, state.StatusRunning)
			}
		} else {
			o.setStatus(s.ID, state.StatusFailed)
			o.failBlockedStages()
			o.bus.Publish(Event{Type: EventRetryExhausted, StageID: s.ID})
		}
	}
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}
