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
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

// phasePlanning is the agent phase name used for planning agents.
const phasePlanning = "planning"

const phaseImplementation = "implementation"
const phaseReview = "review"

const planningContract = `## Output Contract (mandatory)
The plan MUST contain sections: "## Tasks", "## Assumptions", "## Acceptance Criteria".`

const sectionAssumptions = "Assumptions"

var requiredPlanSections = []string{"Tasks", sectionAssumptions, "Acceptance Criteria"}

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
	Store        *state.Store
	Config       config.Config
	Prompts      Prompts
	Runner       executor.Runner // nil = real Executor
	DashboardURL string          // e.g. "http://127.0.0.1:9876"
	ProxyURL     string          // forwarded to executor env as ANTHROPIC_BASE_URL
	ProxyShimDir string          // forwarded to executor env PATH prefix
}

// Orchestrator manages the full lifecycle of a flow run via event loop.
type Orchestrator struct {
	opts     Options
	graph    *Graph
	runner   executor.Runner
	critical *CriticalBus
	ui       *UIBus
	fsm      *FSM
	sems     map[string]interface {
		acquire()
		release()
	} // per-command semaphores
	activeAgents sync.Map // stageID → struct{}: set while an agent goroutine runs
}

// New creates an Orchestrator.
func New(opts Options) *Orchestrator {
	critical := NewCriticalBus(16)
	ui := NewUIBus()

	r := opts.Runner
	if r == nil {
		globalProxyURL, globalShimDir := proxyForCmd(opts.Config.Client.Command, opts.ProxyURL, opts.ProxyShimDir)
		r = executor.New(executor.Config{
			Command:      opts.Config.Client.Command,
			ExtraArgs:    opts.Config.Client.ExtraArgs,
			IdleTimeout:  opts.Config.Executor.IdleTimeout,
			OnAction:     uiActionPublisher(ui, ""),
			ProxyURL:     globalProxyURL,
			ProxyShimDir: globalShimDir,
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
		opts:     opts,
		graph:    NewGraph(opts.Stages),
		runner:   r,
		critical: critical,
		ui:       ui,
		fsm:      NewFSM(opts.Store),
		sems:     sems,
	}
}

// UIBus returns the UIBus for external subscribers (server, WebSocket).
func (o *Orchestrator) UIBus() *UIBus { return o.ui }

// Trigger applies an FSM event to transition a stage's status.
// Returns the new status and whether the transition was applied.
func (o *Orchestrator) Trigger(stageID string, ev FSMEvent, ctx GuardCtx, reason string) (state.StageStatus, bool) {
	to, ok, err := o.fsm.Apply(stageID, ev, ctx, reason)
	if err != nil {
		log.Printf("CRITICAL: FSM Apply %s/%s: %v", stageID, ev, err)
		return o.currentStatus(stageID), false
	}
	if ok {
		ev := Event{Type: EventStageStatusChanged, StageID: stageID, Data: string(to)}
		o.ui.Publish(ev)
		// Wake the event loop so it can check shouldExit(). Non-blocking to avoid deadlock.
		select {
		case o.critical.ch <- ev:
		default:
		}
	}
	return to, ok
}

// SetDashboardURL sets the dashboard URL after the server starts listening.
func (o *Orchestrator) SetDashboardURL(url string) { o.opts.DashboardURL = url }

// FailStage marks a stage as failed with a reason.
func (o *Orchestrator) FailStage(stageID, reason string) {
	o.Trigger(stageID, EvFail, GuardCtx{}, reason)
	o.failBlockedStages()
}

// markAgentActive records that an agent goroutine is running for a stage.
// Called after sem.acquire() so it reflects actively-running agents only.
// Store is idempotent, so double-marking (e.g. goroutine + nested call) is safe.
func (o *Orchestrator) markAgentActive(stageID string) { o.activeAgents.Store(stageID, struct{}{}) }

// markAgentDone clears the active-agent marker for a stage. Called via defer
// before sem.release().
func (o *Orchestrator) markAgentDone(stageID string) { o.activeAgents.Delete(stageID) }

// isAgentActive reports whether an agent goroutine is currently running for a stage.
func (o *Orchestrator) isAgentActive(stageID string) bool {
	_, ok := o.activeAgents.Load(stageID)
	return ok
}

// NotifyAnswer is called by the HTTP handler when the user submits an answer.
// If the agent goroutine is still running (its bash loop is awaiting
// answer.json), we only transition the status — the bash loop will detect the
// file and continue without a restart. If the goroutine has exited, we publish
// to the critical bus so onUserAnswered can restart it.
func (o *Orchestrator) NotifyAnswer(stageID, phase, qID, answer string, fromOptions bool) error {
	if o.isAgentActive(stageID) {
		guardPhase := phaseImplementation
		switch phase {
		case phasePlanning:
			guardPhase = phasePlanning
		case phaseReview:
			guardPhase = phaseReview
		default:
			// phaseImplementation (already set as default above)
		}
		o.Trigger(stageID, EvUserAnswered, GuardCtx{Phase: guardPhase}, "")
		o.ui.Publish(Event{Type: EventUserAnswered, StageID: stageID, Data: map[string]any{
			"id": qID, "phase": phase, "answer": answer,
		}})
		return nil
	}
	return o.critical.Publish(context.Background(), Event{
		Type:    EventUserAnswered,
		StageID: stageID,
		Data:    map[string]any{"id": qID, "phase": phase, "answer": answer},
	})
}

// proxyForCmd возвращает ProxyURL и ProxyShimDir для команды cmd.
// Команда claude использует OAuth-токен (CLAUDE_CODE_OAUTH_TOKEN) и ходит
// напрямую на api.anthropic.com — инжектировать прокси z.ai ей не нужно
// (z.ai не принимает OAuth-токены, только API-ключи).
func proxyForCmd(cmd, proxyURL, shimDir string) (string, string) {
	if cmd == "claude" {
		return "", ""
	}
	return proxyURL, shimDir
}

// runnerFor returns the appropriate Runner for a stage's phase.
// For interactive stages it generates a session id and returns an executor
// configured with --session-id / --resume and AFM_STAGE_DIR env.
func (o *Orchestrator) runnerFor(s flow.Stage, phase string) executor.Runner {
	if !s.Interactive {
		if s.Command == "" {
			return o.runner
		}
		pURL, pShim := proxyForCmd(s.Command, o.opts.ProxyURL, o.opts.ProxyShimDir)
		return executor.New(executor.Config{
			Command:      s.Command,
			IdleTimeout:  o.opts.Config.Executor.IdleTimeout,
			OnAction:     uiActionPublisher(o.ui, s.ID),
			ProxyURL:     pURL,
			ProxyShimDir: pShim,
		})
	}

	stageDir := filepath.Join(o.opts.RunDir, s.ID)
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
	// Interactive stages always need the claude stream-json flags (incl. --verbose,
	// afm bug #1.1). ResolveArgs prepends defaults and dedups user overrides.
	extraArgs := executor.ResolveArgs(o.opts.Config.Client.ExtraArgs)
	pURL, pShim := proxyForCmd(cmd, o.opts.ProxyURL, o.opts.ProxyShimDir)
	return executor.New(executor.Config{
		Command:      cmd,
		ExtraArgs:    extraArgs,
		IdleTimeout:  o.opts.Config.Executor.IdleTimeout,
		OnAction:     uiActionPublisher(o.ui, s.ID),
		SessionID:    sessionID,
		Resume:       resume,
		StageDir:     stageDir,
		ProxyURL:     pURL,
		ProxyShimDir: pShim,
	})
}

func (o *Orchestrator) runnerForFallback(s flow.Stage) executor.Runner {
	if s.Command == "" {
		return o.runner
	}
	pURL, pShim := proxyForCmd(s.Command, o.opts.ProxyURL, o.opts.ProxyShimDir)
	return executor.New(executor.Config{
		Command:      s.Command,
		IdleTimeout:  o.opts.Config.Executor.IdleTimeout,
		OnAction:     uiActionPublisher(o.ui, s.ID),
		ProxyURL:     pURL,
		ProxyShimDir: pShim,
	})
}

func uiActionPublisher(ui *UIBus, stageID string) func(string, string) {
	return func(tool, detail string) {
		ui.Publish(Event{Type: EventAgentAction, StageID: stageID, Data: map[string]string{
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
	o.startPlanningForPending(ctx)
	o.startQuestionPoller(ctx) // file-based dialog poller

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-o.critical.Recv():
			if err := o.handleEvent(ctx, ev); err != nil {
				return err
			}
			if o.shouldExit() {
				return nil
			}
		}
	}
}

// startQuestionPoller launches a goroutine that scans active stage directories
// every second for new *.question.json files (file-based dialog protocol).
func (o *Orchestrator) startQuestionPoller(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		processed := map[string]bool{} // "stageID|phase|id" → true
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				o.pollQuestions(processed)
			}
		}
	}()
}

// pollQuestions scans each active stage directory for unanswered question files.
// For each new file: writes it to dialog.jsonl (for UI history) and publishes
// EventAskUser to transition the stage to awaiting_user_input.
func (o *Orchestrator) pollQuestions(processed map[string]bool) {
	snap := o.opts.Store.Snapshot()
	for stageID, st := range snap.Stages {
		switch st.Status {
		case state.StatusPlanning, state.StatusRunning, state.StatusRevising,
			state.StatusRetrying, state.StatusAwaitingUserInput:
		default:
			continue
		}
		stageDir := filepath.Join(o.opts.RunDir, stageID)
		questions, err := mcp.FindUnansweredQuestions(stageDir)
		if err != nil {
			continue
		}
		for _, q := range questions {
			key := stageID + "|" + q.Phase + "|" + q.ID
			if processed[key] {
				continue
			}
			processed[key] = true
			// Write to dialog.jsonl for history (idempotent via FindEntry check).
			dialogPath := filepath.Join(stageDir, q.Phase+".dialog.jsonl")
			if e, _ := mcp.FindEntry(dialogPath, q.ID); e == nil {
				_ = mcp.AppendQuestion(dialogPath, mcp.Question{
					ID:          q.ID,
					Question:    q.Question,
					Options:     q.Options,
					AllowCustom: q.AllowCustom,
				})
			}
			// Notify UI and transition stage status.
			o.ui.Publish(Event{
				Type:    EventAskUser,
				StageID: stageID,
				Data: map[string]any{
					"id": q.ID, "phase": q.Phase, "question": q.Question,
					"options": q.Options, "allow_custom": q.AllowCustom,
				},
			})
			o.Trigger(stageID, EvAskUser, GuardCtx{Phase: q.Phase}, "")
		}
		// No open question in stageDir: if this is an interactive stage, check
		// whether the agent wrote one elsewhere (dialog contract violation).
		// Fail fast with a clear reason instead of hanging forever (afm bug-2).
		if len(questions) == 0 {
			if stage := o.graph.Stage(stageID); stage != nil && stage.Interactive {
				if reason, ok := detectDialogViolation(stageDir); ok {
					o.FailStage(stageID, reason)
				}
			}
		}
	}
}

// detectDialogViolation scans the agent's stream-json logs (<phase>.jsonl) for a
// Write of a *.question.json file OUTSIDE the stage directory. Such a write
// violates the file-based dialog contract: the poller and dashboard only look
// inside stageDir, so a misplaced question hangs the stage forever. Returns a
// human-readable reason when a violation is found.
func detectDialogViolation(stageDir string) (string, bool) {
	for _, phase := range []string{phasePlanning, phaseImplementation, phaseReview} {
		jsonlPath := filepath.Join(stageDir, phase+".jsonl")
		for _, f := range executor.WrittenFiles(jsonlPath) {
			if !strings.HasSuffix(filepath.Base(f), ".question.json") {
				continue
			}
			if !pathInside(f, stageDir) {
				return fmt.Sprintf("dialog protocol violation: question written to %s, expected %s", f, stageDir), true
			}
		}
	}
	return "", false
}

// pathInside reports whether file is located inside dir. Both are resolved to
// absolute paths the same way (filepath.Abs, no symlink resolution), so they
// stay in a consistent form — the agent's Write paths and stageDir both originate
// from the same source (AFM_STAGE_DIR), so a consistent resolution is
// sufficient and avoids EvalSymlinks' failure on not-yet-existing files.
func pathInside(file, dir string) bool {
	absFile, err := filepath.Abs(file)
	if err != nil {
		absFile = filepath.Clean(file)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = filepath.Clean(dir)
	}
	if absDir != string(filepath.Separator) {
		absDir += string(filepath.Separator)
	}
	return strings.HasPrefix(absFile+string(filepath.Separator), absDir)
}

// Approve approves a stage plan.
func (o *Orchestrator) Approve(ctx context.Context, stageID string) error {
	return o.critical.Publish(ctx, Event{Type: EventApproved, StageID: stageID})
}

// Revise sends feedback to re-plan a stage.
func (o *Orchestrator) Revise(ctx context.Context, stageID, feedback string) error {
	return o.critical.Publish(ctx, Event{Type: EventRevised, StageID: stageID, Data: feedback})
}

// Retry retries a failed stage by transitioning it to pending and restarting.
func (o *Orchestrator) Retry(ctx context.Context, stageID string) error {
	return o.critical.Publish(ctx, Event{Type: EventManualRetry, StageID: stageID})
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
		o.Trigger(ev.StageID, EvAskUser, GuardCtx{Phase: agentType}, "")
		return nil
	}

	switch agentType {
	case phasePlanning:
		// Ignore stale completion if stage already left planning state
		// (e.g. approved, done, or restarted by onUserAnswered).
		if current != state.StatusPlanning && current != state.StatusRetrying {
			return nil
		}
		o.Trigger(ev.StageID, EvPlanReady, GuardCtx{}, "")
		o.tryActivatePrePlanned(ctx)
	case phaseImplementation:
		if current != state.StatusRunning && current != state.StatusRetrying {
			return nil
		}
		o.Trigger(ev.StageID, EvComplete, GuardCtx{}, "")
		o.failBlockedStages()
		o.startPlanningForUnblocked(ctx)
		o.startReadyStages(ctx)
		o.tryActivatePrePlanned(ctx)
	default:
		// review or unknown agent type: no status change needed
	}
	return nil
}

// hasOpenQuestion reports whether stageDir contains a *.question.json file
// for the given phase that has no corresponding *.answer.json yet.
func (o *Orchestrator) hasOpenQuestion(stageID, phase string) bool {
	if phase == "" {
		return false
	}
	questions, err := mcp.FindUnansweredQuestions(filepath.Join(o.opts.RunDir, stageID))
	if err != nil {
		return false
	}
	for _, q := range questions {
		if q.Phase == phase {
			return true
		}
	}
	return false
}

// onUserAnswered resumes a stage that was paused on awaiting_user_input.
// If the agent is still running (its bash loop is waiting for answer.json),
// NotifyAnswer already transitioned the status — this is a no-op.
// If the agent exited before the user answered, we restart it here.
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

	// Agent exited before the user answered. Restart it so it can read
	// answer.json (bash loop exits immediately since the file now exists).
	switch phase {
	case phasePlanning:
		o.Trigger(ev.StageID, EvUserAnswered, GuardCtx{Phase: phasePlanning}, "")
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			o.markAgentActive(st.ID)
			defer func() {
				o.markAgentDone(st.ID)
				sem.release()
			}()
			o.runPlanningAgent(ctx, st)
		}(*stage)
	case phaseImplementation:
		o.Trigger(ev.StageID, EvUserAnswered, GuardCtx{Phase: phaseImplementation}, "")
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			o.markAgentActive(st.ID)
			defer func() {
				o.markAgentDone(st.ID)
				sem.release()
			}()
			o.runImplementationAgent(ctx, st)
		}(*stage)
	case phaseReview:
		o.Trigger(ev.StageID, EvUserAnswered, GuardCtx{Phase: phaseReview}, "")
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			o.markAgentActive(st.ID)
			defer func() {
				o.markAgentDone(st.ID)
				sem.release()
			}()
			o.runReviewAgent(ctx, st)
		}(*stage)
	default:
		return fmt.Errorf("unexpected phase: %q", phase)
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
		o.Trigger(ev.StageID, EvComplete, GuardCtx{}, "planning-only stage")
		o.startPlanningForUnblocked(ctx)
		o.startReadyStages(ctx)
		o.tryActivatePrePlanned(ctx)
		return nil
	}
	o.Trigger(ev.StageID, EvApprove, GuardCtx{}, "")
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

	o.Trigger(stageID, EvRevise, GuardCtx{}, feedback)

	stageDir := filepath.Join(o.opts.RunDir, stageID)
	if _, err := state.VersionPlan(stageDir); err != nil {
		return fmt.Errorf("version plan for %s: %w", stageID, err)
	}
	if err := state.SaveFeedback(stageDir, feedback); err != nil {
		return fmt.Errorf("save feedback for %s: %w", stageID, err)
	}

	stage := o.graph.Stage(stageID)
	if stage != nil {
		s := *stage
		sem := o.semFor(s)
		go func() {
			sem.acquire()
			o.markAgentActive(stageID)
			defer func() {
				o.markAgentDone(stageID)
				sem.release()
			}()
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

	// Manual retry of an interactive stage must start a fresh Claude session:
	// a leftover <phase>.session.json may reference a conversation that was
	// never created (phantom), which makes claude fail with "No conversation
	// found". Clear all phase sessions for this stage.
	//
	// Also truncate <phase>.jsonl: detectDialogViolation re-scans the raw
	// stream-json log every poll tick, and a *.question.json Write from a
	// previous (violating) run would otherwise re-fire instantly and make the
	// stage un-retryable. Truncating here (before re-activation) is race-free
	// because the poller skips stages in non-active states.
	if stage.Interactive {
		stageDir := filepath.Join(o.opts.RunDir, stageID)
		for _, ph := range []string{phasePlanning, phaseImplementation, phaseReview} {
			_ = os.Remove(sessionFile(stageDir, ph))
			_ = os.Truncate(filepath.Join(stageDir, ph+".jsonl"), 0)
		}
	}

	if _, ok := o.Trigger(stageID, EvManualRetry, GuardCtx{}, ""); !ok {
		return nil
	}

	if !stage.NeedsPlanning() {
		stageDir := filepath.Join(o.opts.RunDir, stageID)
		planPath := filepath.Join(stageDir, "plan.md")
		if _, err := os.Stat(planPath); err != nil {
			// plan.md not yet on disk — try to copy it from stage.Plan source.
			if !o.depsDone(*stage) {
				return nil
			}
			if stage.Plan == "" {
				o.Trigger(stageID, EvFail, GuardCtx{}, "no plan.md and no plan source configured")
				return nil
			}
			if err := os.MkdirAll(stageDir, 0755); err != nil {
				o.Trigger(stageID, EvFail, GuardCtx{}, "mkdir failed")
				return nil
			}
			if err := copyFile(stage.Plan, planPath); err != nil {
				o.Trigger(stageID, EvFail, GuardCtx{}, "copy plan failed: "+err.Error())
				return nil
			}
		}
		o.Trigger(stageID, EvReady, GuardCtx{}, "")
		o.Trigger(stageID, EvStartRun, GuardCtx{}, "")
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			o.markAgentActive(st.ID)
			defer func() {
				o.markAgentDone(st.ID)
				sem.release()
			}()
			o.runImplementationAgent(ctx, st)
		}(*stage)
		o.startReadyStages(ctx)
		return nil
	}

	stageDir := filepath.Join(o.opts.RunDir, stageID)
	planPath := filepath.Join(stageDir, "plan.md")
	if _, err := os.Stat(planPath); err == nil {
		o.Trigger(stageID, EvReady, GuardCtx{}, "")
		o.Trigger(stageID, EvStartRun, GuardCtx{}, "")
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			o.markAgentActive(st.ID)
			defer func() {
				o.markAgentDone(st.ID)
				sem.release()
			}()
			o.runImplementationAgent(ctx, st)
		}(*stage)
	} else {
		// Deps not done — stay pending; planning starts automatically
		// via startPlanningForUnblocked once dependencies complete.
		if !stage.EagerPlanning && !o.depsDone(*stage) {
			return nil
		}
		// Synchronous transition guards against double start
		// (matches startPlanningForUnblocked pattern).
		if _, ok := o.Trigger(stageID, EvStartPlanning, GuardCtx{Stage: *stage}, "manual retry"); !ok {
			return nil
		}
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			o.markAgentActive(st.ID)
			defer func() {
				o.markAgentDone(st.ID)
				sem.release()
			}()
			o.runPlanningAgent(ctx, st)
		}(*stage)
	}

	return nil
}

// depsDone checks whether all dependencies of a stage are in StatusDone.
func (o *Orchestrator) depsDone(s flow.Stage) bool {
	for _, dep := range s.DependsOn {
		if o.opts.Store.Get(dep) != state.StatusDone {
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

		current := o.opts.Store.Get(s.ID)

		if current != state.StatusPending {
			continue
		}

		if !o.depsDone(s) {
			continue
		}

		stageDir := filepath.Join(o.opts.RunDir, s.ID)
		if err := os.MkdirAll(stageDir, 0755); err != nil {
			o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
			continue
		}
		dst := filepath.Join(stageDir, "plan.md")
		if err := copyFile(s.Plan, dst); err != nil {
			o.Trigger(s.ID, EvFail, GuardCtx{}, "copy plan failed")
			continue
		}
		o.Trigger(s.ID, EvReady, GuardCtx{}, "")
	}

	// Newly activated stages may now be ready to run.
	o.startReadyStages(ctx)
}

// startPlanningForUnblocked starts planning for pending stages whose
// dependencies are all done. Stages with eager_planning start at flow
// start and are never gated here.
func (o *Orchestrator) startPlanningForUnblocked(ctx context.Context) {
	for _, s := range o.opts.Stages {
		if !s.NeedsPlanning() {
			continue
		}
		if o.opts.Store.Get(s.ID) != state.StatusPending {
			continue
		}
		if !o.depsDone(s) {
			continue
		}
		// Synchronous transition out of pending guards against double
		// start: a second call sees "planning" and skips the stage.
		if _, ok := o.Trigger(s.ID, EvStartPlanning, GuardCtx{Stage: s}, "deps done"); !ok {
			continue
		}
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			o.markAgentActive(st.ID)
			defer func() {
				o.markAgentDone(st.ID)
				sem.release()
			}()
			o.runPlanningAgent(ctx, st)
		}(s)
	}
}

// startReadyStages starts implementation for stages whose dependencies are done.
func (o *Orchestrator) startReadyStages(ctx context.Context) {
	snap := o.opts.Store.Snapshot()
	statuses := make(map[string]state.StageStatus, len(snap.Stages))
	for id, s := range snap.Stages {
		statuses[id] = s.Status
	}

	ready := o.graph.ReadyStages(statuses)
	for _, id := range ready {
		stage := o.graph.Stage(id)
		if stage == nil {
			continue
		}
		o.Trigger(id, EvStartRun, GuardCtx{}, "")
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			o.markAgentActive(st.ID)
			defer func() {
				o.markAgentDone(st.ID)
				sem.release()
			}()
			o.runImplementationAgent(ctx, st)
		}(*stage)
	}
}

func (o *Orchestrator) runPlanningAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
		return
	}

	// Defensive: may be a no-op if the caller already transitioned
	// the stage to "planning" (e.g. startPlanningForUnblocked).
	o.Trigger(s.ID, EvStartPlanning, GuardCtx{Stage: s}, "")

	o.runWithRetry(ctx, s, phasePlanning, func(retryContext string) error {
		depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages)
		artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
		if artErr != nil {
			log.Printf("WARN: collect artifacts for %s planning: %v", s.ID, artErr)
		}
		prompt := prompts.Build(prompts.Inputs{
			Template:         o.opts.Prompts.Planning,
			Stage:            s,
			PhaseAgent:       prompts.AgentPlanning,
			DependencyPlans:  depPlans,
			Artifacts:        artCtx,
			StageDir:         stageDir,
			Interactive:      s.Interactive,
			OutputContractMD: planningContract,
			RetryContext:     retryContext,
		})
		outFile := filepath.Join(stageDir, "plan.md")
		logFile := filepath.Join(stageDir, "planning.log")

		r := o.runnerFor(s, phasePlanning)
		if err := r.RunPlanning(ctx, s.Name, prompt, outFile, logFile); err != nil {
			return err
		}

		planMD, _ := os.ReadFile(outFile)
		issues := prompts.ValidatePlan(string(planMD), requiredPlanSections)
		if !issues.IsClean() {
			if adoptWrittenPlan(logFile, outFile) {
				return nil
			}
			if s.Interactive {
				return nil
			}
			if err := o.rePromptMissingSections(ctx, s, string(planMD), issues.MissingSections, outFile); err != nil {
				return err
			}
		}
		return nil
	}, func() error {
		return checkPlanCompletionFor(stageDir, s.Interactive)
	})
}

func (o *Orchestrator) rePromptMissingSections(ctx context.Context, s flow.Stage, prevPlan string, missing []string, outFile string) error {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	prompt := fmt.Sprintf(
		"Your previous plan was missing required sections: %s.\nAdd ONLY the missing sections to the existing plan below. Do not rewrite the rest.\n\n<previous_plan>\n%s\n</previous_plan>",
		strings.Join(missing, ", "),
		prompts.EscapeTagsForReprompt(prevPlan),
	)
	logFile := filepath.Join(stageDir, "planning-reprompt.log")
	r := o.runnerFor(s, phasePlanning)
	if err := r.RunPlanning(ctx, s.Name, prompt, outFile, logFile); err != nil {
		return err
	}
	planMD, _ := os.ReadFile(outFile)
	issues := prompts.ValidatePlan(string(planMD), requiredPlanSections)
	if !issues.IsClean() {
		if adoptWrittenPlan(logFile, outFile) {
			return nil
		}
		return &MissingSectionsError{Missing: issues.MissingSections}
	}
	return nil
}

func (o *Orchestrator) runPlanningWithFeedback(ctx context.Context, s flow.Stage) {
	o.markAgentActive(s.ID)
	defer o.markAgentDone(s.ID)

	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	o.Trigger(s.ID, EvStartPlanning, GuardCtx{Stage: s}, "")

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

		depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages)
		artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
		if artErr != nil {
			log.Printf("WARN: collect artifacts for %s revise: %v", s.ID, artErr)
		}
		prompt := prompts.Build(prompts.Inputs{
			Template:         o.opts.Prompts.Planning,
			Stage:            s,
			PhaseAgent:       prompts.AgentPlanning,
			DependencyPlans:  depPlans,
			Artifacts:        artCtx,
			PreviousPlan:     prevPlan,
			Feedback:         string(feedbackData),
			StageDir:         stageDir,
			Interactive:      s.Interactive,
			OutputContractMD: planningContract,
			RetryContext:     retryContext,
		})
		outFile := filepath.Join(stageDir, "plan.md")
		logFile := filepath.Join(stageDir, "planning-revision.log")

		r := o.runnerFor(s, phasePlanning)
		if err := r.RunPlanning(ctx, s.Name, prompt, outFile, logFile); err != nil {
			return err
		}
		planMD, _ := os.ReadFile(outFile)
		issues := prompts.ValidatePlan(string(planMD), requiredPlanSections)
		if !issues.IsClean() {
			if adoptWrittenPlan(logFile, outFile) {
				return nil
			}
			if s.Interactive {
				return nil
			}
			if err := o.rePromptMissingSections(ctx, s, string(planMD), issues.MissingSections, outFile); err != nil {
				return err
			}
		}
		return nil
	}, func() error {
		return checkPlanCompletionFor(stageDir, s.Interactive)
	})
}

func (o *Orchestrator) runImplementationAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	o.runWithRetry(ctx, s, phaseImplementation, func(retryContext string) error {
		planData, err := os.ReadFile(filepath.Join(stageDir, "plan.md"))
		if err != nil {
			return err
		}

		depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages)
		artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
		if artErr != nil {
			log.Printf("WARN: collect artifacts for %s impl: %v", s.ID, artErr)
		}

		// Format output artifact requirements
		if len(s.Artifacts) > 0 {
			var buf strings.Builder
			buf.WriteString("\n\nRequired output artifacts (MUST exist at these paths when stage finishes):\n\n")
			for _, art := range s.Artifacts {
				dst := art.Path
				if strings.HasPrefix(art.Path, "./") {
					dst = filepath.Join(stageDir, art.Path[2:])
				}
				desc := ""
				if art.Description != "" {
					desc = " — " + art.Description
				}
				fmt.Fprintf(&buf, "- %s%s → %s\n", art.Name, desc, dst)
			}
			artCtx += buf.String()
		}

		stageDirNote := fmt.Sprintf("\n\nStage directory for .done file: %s", stageDir)
		if s.Verify != "" {
			stageDirNote += fmt.Sprintf("\n\nVerify command (runs automatically after you finish; it MUST exit 0, "+
				"so run it yourself before creating .done):\n%s", s.Verify)
		}
		prompt := prompts.Build(prompts.Inputs{
			Template:        o.opts.Prompts.Implementation,
			Stage:           s,
			PhaseAgent:      prompts.AgentImplementation,
			DependencyPlans: depPlans,
			Artifacts:       artCtx,
			Plan:            string(planData),
			StageDir:        stageDir,
			Interactive:     s.Interactive,
			RetryContext:    retryContext + stageDirNote,
		})
		logFile := filepath.Join(stageDir, "implementation.log")

		r := o.runnerFor(s, phaseImplementation)
		if err := r.RunAgent(ctx, string(s.ImplAgent()), s.Name, prompt, logFile); err != nil {
			return err
		}

		if s.HasAgent(flow.AgentReview) {
			reviewPrompt := prompts.Build(prompts.Inputs{
				Template:        o.opts.Prompts.Review,
				Stage:           s,
				PhaseAgent:      prompts.AgentReview,
				DependencyPlans: depPlans,
				Artifacts:       artCtx,
				StageDir:        stageDir,
				Interactive:     s.Interactive,
			})
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

func (o *Orchestrator) runReviewAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
		return
	}

	depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages)
	artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
	if artErr != nil {
		log.Printf("WARN: collect artifacts for %s review: %v", s.ID, artErr)
	}

	o.runWithRetry(ctx, s, phaseReview, func(retryContext string) error {
		reviewPrompt := prompts.Build(prompts.Inputs{
			Template:        o.opts.Prompts.Review,
			Stage:           s,
			PhaseAgent:      prompts.AgentReview,
			DependencyPlans: depPlans,
			Artifacts:       artCtx,
			StageDir:        stageDir,
			Interactive:     s.Interactive,
			RetryContext:    retryContext,
		})
		reviewLog := filepath.Join(stageDir, "review.log")
		rr := o.runnerFor(s, phaseReview)
		return rr.RunAgent(ctx, phaseReview, s.Name, reviewPrompt, reviewLog)
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
			current := o.opts.Store.Get(s.ID)

			if current != state.StatusPending {
				continue
			}

			for _, dep := range s.DependsOn {
				if o.opts.Store.Get(dep) == state.StatusFailed {
					o.Trigger(s.ID, EvBlockedByDep, GuardCtx{}, "dep failed")
					changed = true
					break
				}
			}
		}
	}
}

func (o *Orchestrator) allTerminal() bool {
	snap := o.opts.Store.Snapshot()
	if len(snap.Stages) == 0 {
		return true
	}
	for _, s := range snap.Stages {
		if !IsTerminal(s.Status) {
			return false
		}
	}
	return true
}

// shouldExit reports whether the orchestrator loop should stop.
// Without a dashboard, any terminal state (done or failed) is final.
// With a dashboard, exit only when all stages are done — failed stages stay
// visible so the user can retry them without restarting the process.
func (o *Orchestrator) shouldExit() bool {
	if !o.allTerminal() {
		return false
	}
	if o.opts.DashboardURL == "" {
		return true
	}
	snap := o.opts.Store.Snapshot()
	return snap.AllDone()
}

func (o *Orchestrator) currentStatus(id string) state.StageStatus {
	return o.opts.Store.Get(id)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}
