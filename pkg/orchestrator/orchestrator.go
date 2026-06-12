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
}

// New creates an Orchestrator.
func New(opts Options) *Orchestrator {
	critical := NewCriticalBus(16)
	ui := NewUIBus()

	r := opts.Runner
	if r == nil {
		r = executor.New(executor.Config{
			Command:     opts.Config.Client.Command,
			ExtraArgs:   opts.Config.Client.ExtraArgs,
			IdleTimeout: opts.Config.Executor.IdleTimeout,
			OnAction:    uiActionPublisher(ui, ""),
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
		// Wake the event loop so it can check allTerminal(). Non-blocking to avoid deadlock.
		select {
		case o.critical.ch <- ev:
		default:
		}
	}
	return to, ok
}

// SetDashboardURL sets the dashboard URL after the server starts listening.
func (o *Orchestrator) SetDashboardURL(url string) { o.opts.DashboardURL = url }

// PublishCriticalForTest publishes an event to the critical bus for test use.
func (o *Orchestrator) PublishCriticalForTest(ev Event) {
	_ = o.critical.Publish(context.Background(), ev)
}

// FailStage marks a stage as failed with a reason.
func (o *Orchestrator) FailStage(stageID, reason string) {
	o.Trigger(stageID, EvFail, GuardCtx{}, reason)
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
			OnAction:    uiActionPublisher(o.ui, s.ID),
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
		OnAction:    uiActionPublisher(o.ui, s.ID),
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
		OnAction:    uiActionPublisher(o.ui, s.ID),
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

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-o.critical.Recv():
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
		o.Trigger(ev.StageID, EvUserAnswered, GuardCtx{Phase: phasePlanning}, "")
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			defer sem.release()
			o.runPlanningAgent(ctx, st)
		}(*stage)
	default:
		o.Trigger(ev.StageID, EvUserAnswered, GuardCtx{Phase: phaseImplementation}, "")
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
		o.Trigger(ev.StageID, EvComplete, GuardCtx{}, "planning-only stage")
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

	if _, ok := o.Trigger(stageID, EvManualRetry, GuardCtx{}, ""); !ok {
		return nil
	}

	if !stage.NeedsPlanning() {
		o.Trigger(stageID, EvReady, GuardCtx{}, "")
		o.Trigger(stageID, EvStartRun, GuardCtx{}, "")
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
		o.Trigger(stageID, EvReady, GuardCtx{}, "")
		o.Trigger(stageID, EvStartRun, GuardCtx{}, "")
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
			defer sem.release()
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
			if err := o.rePromptMissingSections(ctx, s, string(planMD), issues.MissingSections, outFile); err != nil {
				return err
			}
		}
		return nil
	}, func() error {
		return checkPlanCompletion(stageDir)
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
			if err := o.rePromptMissingSections(ctx, s, string(planMD), issues.MissingSections, outFile); err != nil {
				return err
			}
		}
		return nil
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
				Template:   o.opts.Prompts.Review,
				Stage:      s,
				PhaseAgent: prompts.AgentReview,
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
