package orchestrator

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/agentjobs"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memorypipeline"
	"github.com/akopichin/afm/pkg/state"
)

// helperJobPhase is the phase of a helper or run agent turn, as its executor config names it.
const helperJobPhase = "helper"

// jobConfig makes the executor measure every Run* call as a job with meta's identity. waiter, when set, is the
// participant whose turn may end in a mesh wait instead of an answer. No journal, no jobs.
func (o *Orchestrator) jobConfig(meta agentjobs.JobMeta, waiter agentcomm.ParticipantID) *executor.JobConfig {
	if o.opts.Jobs == nil {
		return nil
	}
	meta.RunID = o.opts.RunID
	cfg := &executor.JobConfig{Meta: meta, Sink: o.opts.Jobs, Segment: o.jobSegment}
	if waiter != "" {
		cfg.Yielded = o.turnYieldCheck(waiter, meta.AgentKind == accounting.AgentKindStage)
	}
	return cfg
}

// turnYieldCheck decides a clean exit exactly as the runtime will after it (turnYielded): a stage author and a
// helper in native schema mode park for the waits of the turn's episode (byWaits), any other helper yields unless
// it answered. The waits are read now, before the process starts.
func (o *Orchestrator) turnYieldCheck(waiter agentcomm.ParticipantID, byWaits bool) func(finalText string) bool {
	before := o.meshWaitStatusesOf(waiter)
	return func(finalText string) bool { return o.turnYielded(waiter, before, finalText, byWaits) }
}

// jobSegment places a job on the run's active timeline as the run state knows it at the job's start. A run that
// is not alive (not begun yet, or already ended) has no timeline to offer.
func (o *Orchestrator) jobSegment(startedAt time.Time) agentjobs.Segment {
	var seg agentjobs.Segment
	o.opts.Store.View(func(rs *state.RunState) {
		if rs.EndedAt != nil || rs.RunStartedAt.IsZero() {
			return
		}
		seg = agentjobs.Segment{
			RunStartedAt: rs.RunStartedAt,
			BaseMS:       rs.ElapsedAccumulatedMs,
			OffsetMS:     rs.ElapsedAccumulatedMs + max(startedAt.Sub(rs.RunStartedAt).Milliseconds(), 0),
		}
	})
	return seg
}

// withHint adds the configured model of cmd; the model the agent reports lands in usage.jsonl.
func (o *Orchestrator) withHint(meta agentjobs.JobMeta, cmd string) agentjobs.JobMeta {
	hint := o.usageHintFor(cmd)
	meta.Channel, meta.Model = hint.Channel, hint.Model
	return meta
}

// stageAgentMeta is the identity every job of a stage's own agent shares.
func stageAgentMeta(stageID, title string) agentjobs.JobMeta {
	return agentjobs.JobMeta{
		AgentID: string(meshStageParticipantID(stageID)), AgentKind: accounting.AgentKindStage,
		StageID: stageID, RootStageID: stageID, Title: title,
	}
}

func stageLabel(s flow.Stage) string {
	if s.Name != "" {
		return s.Name
	}
	return s.ID
}

func (o *Orchestrator) stageLabelByID(stageID string) string {
	if s := o.reg().Stage(stageID); s != nil {
		return stageLabel(*s)
	}
	return stageID
}

// stageJobMeta is one author turn of a stage: "<Stage> · <Phase>", e.g. "Build · Autonomous execution".
func (o *Orchestrator) stageJobMeta(s flow.Stage, phase, cmd string) agentjobs.JobMeta {
	label := strings.ReplaceAll(phase, "_", " ")
	if label != "" {
		label = strings.ToUpper(label[:1]) + label[1:]
	}
	meta := stageAgentMeta(s.ID, stageLabel(s)+" · "+label)
	meta.Phase = phase
	return o.withHint(meta, cmd)
}

// lastSessionJob is the latest job that ran in the CLI session, the one a resumed turn continues.
func (o *Orchestrator) lastSessionJob(sessionID string) string {
	records := o.opts.Jobs.Snapshot().Records
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Type == agentjobs.TypeStarted && records[i].SessionID == sessionID {
			return records[i].JobID
		}
	}
	return ""
}

// verifyJobMeta is one AI verify step: an agent of its own for every pass of that step. step is the step's
// 1-based number among all verify steps of the stage, as the report shows it.
func (o *Orchestrator) verifyJobMeta(s flow.Stage, step int, cmd string) agentjobs.JobMeta {
	return o.withHint(agentjobs.JobMeta{
		AgentID: fmt.Sprintf("verify:%s#%d", s.ID, step), AgentKind: accounting.AgentKindVerify,
		StageID: s.ID, RootStageID: s.ID, Phase: verifyExecutionLabel, VerifyStep: step,
		Title: fmt.Sprintf("Verify · step %d", step),
	}, cmd)
}

// verifyStepKey carries the number of the verify step being run to the executor that measures it; the agent
// runner seam has no parameter for it.
type verifyStepKey struct{}

func withVerifyStep(ctx context.Context, step int) context.Context {
	return context.WithValue(ctx, verifyStepKey{}, step)
}

func verifyStepOf(ctx context.Context) int {
	step, _ := ctx.Value(verifyStepKey{}).(int)
	return step
}

// taskJobMeta is one turn of a helper or run agent, keyed by its mesh participant ID (a run agent of an old
// journal keeps "run:<profile>"). The title is the task's own name, never a message.
func (o *Orchestrator) taskJobMeta(rs state.RunState, taskID assistants.TaskID, cmd string) agentjobs.JobMeta {
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok {
		return o.withHint(agentjobs.JobMeta{
			AgentID: string(meshHelperParticipantID(taskID)), AgentKind: accounting.AgentKindHelper,
			TaskID: string(taskID), Phase: helperJobPhase,
		}, cmd)
	}
	meta := agentjobs.JobMeta{
		AgentID: string(meshParticipantForTask(rs, t.Task)), AgentKind: accounting.AgentKindHelper,
		StageID: t.RootStageID, RootStageID: t.RootStageID, TaskID: string(taskID), ProfileID: string(t.ProfileID),
		Phase: helperJobPhase, Turn: int(t.Turn), Title: o.RedactSecrets(t.Title),
	}
	if t.Scope == assistants.ScopeRun {
		meta.AgentKind = accounting.AgentKindRunAgent
	}
	if t.ParentTaskID != nil {
		meta.ParentTaskID = string(*t.ParentTaskID)
	}
	return o.withHint(meta, cmd)
}

// repairJob measures a format repair as a job of the agent whose answer it repairs.
func (o *Orchestrator) repairJob(owner agentjobs.JobMeta, cmd string) *executor.JobConfig {
	title := owner.Title
	if title == "" {
		title = owner.AgentID
	}
	meta := owner
	meta.Phase = phaseJSONFix
	meta.Turn, meta.SessionID, meta.PrevJobID = 0, "", ""
	meta.Title = "Format repair · " + title
	return o.jobConfig(o.withHint(meta, cmd), "")
}

// memoryJob measures one memory agent call: reflect belongs to its stage, the distilling steps to run overhead.
func (o *Orchestrator) memoryJob(spec memorypipeline.AgentSpec) *executor.JobConfig {
	meta := agentjobs.JobMeta{AgentID: accounting.RunOverheadAgentID, AgentKind: accounting.AgentKindRunOverhead}
	if spec.Scope != memorypipeline.ScopeRunOverhead && spec.StageID != "" {
		meta = stageAgentMeta(spec.StageID, "")
	}
	meta.Phase = spec.Phase
	meta.Title = "Memory · " + spec.Kind
	return o.jobConfig(o.withHint(meta, o.opts.Config.Client.Command), "")
}

// AssistantJob measures every Assistant turn in the Assistant's own journal: it stays outside the run total
// and gets no timeline place once the flow has ended.
func (o *Orchestrator) AssistantJob(journal *agentjobs.Store) *executor.JobConfig {
	if journal == nil {
		return nil
	}
	meta := agentjobs.JobMeta{RunID: o.opts.RunID, AgentID: string(o.SideAgentParticipantID()), AgentKind: accounting.AgentKindAssistant}
	return &executor.JobConfig{Meta: o.withHint(meta, o.opts.Config.Client.Command), Sink: journal, Segment: o.jobSegment}
}

// stageSnapshot is the stage's cumulative jobs and usage as of at. runningTurns must be read before the journal:
// a running helper turn whose job is not on it yet may have started before at.
func (o *Orchestrator) stageSnapshot(stageID string, at time.Time, runningTurns map[string]int) accounting.StageSnapshot {
	var jobs agentjobs.Snapshot // empty reads as a run without a journal
	if o.opts.Jobs != nil {
		jobs = o.opts.Jobs.Snapshot()
	}
	src := accounting.AnalyticsSources{Usage: o.opts.Accounting, Jobs: frozenJobs(jobs), Meta: o.AnalyticsMeta}
	snap := accounting.NewAnalytics(src).Facts().StageSnapshot(stageID, at, startMissing(jobs, runningTurns))
	if o.opts.Accounting == nil {
		// Usage is collected whatever accounting shows, so a run without a ledger could not open it.
		snap.Usage = accounting.StageUsage{Health: accounting.HealthUnavailable, Coverage: accounting.CoverageNone,
			Reasons: []string{accounting.ReasonUsageUnavailable}}
	}
	return snap
}

// frozenJobs hands the read model the journal snapshot the pending starts were judged against.
type frozenJobs agentjobs.Snapshot

func (j frozenJobs) Snapshot() agentjobs.Snapshot { return agentjobs.Snapshot(j) }

// startMissing reports a running helper turn that has no started job on the journal.
func startMissing(jobs agentjobs.Snapshot, runningTurns map[string]int) bool {
	started := map[string]bool{}
	for _, rec := range jobs.Records {
		if turn, ok := runningTurns[rec.TaskID]; ok && rec.Type == agentjobs.TypeStarted && rec.Turn == turn {
			started[rec.TaskID] = true
		}
	}
	return len(started) < len(runningTurns)
}

// stageStartStatuses mirror the statuses whose first entry starts a stage's clock (state.StageTiming).
var stageStartStatuses = map[state.StageStatus]bool{
	state.StatusPlanning: true, state.StatusAwaitingApproval: true, state.StatusRevising: true, state.StatusRunning: true,
}

// analyticsMetaCache keeps the last metadata until the journal or the stage registry moves.
type analyticsMetaCache struct {
	mu       sync.Mutex
	built    bool
	seq      uint64
	graphRev int64
	meta     accounting.AnalyticsMeta
}

// AnalyticsMeta describes the run's stages and participants for the usage read model. Every change of them is
// journaled, so it is rebuilt only when the journal or the registry moved and a dashboard poll stays cheap.
func (o *Orchestrator) AnalyticsMeta() accounting.AnalyticsMeta {
	var seq uint64
	o.opts.Store.View(func(rs *state.RunState) { seq = rs.LastSeq })
	graphRev := o.reg().Revision()

	c := &o.analyticsMeta
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.built && c.seq == seq && c.graphRev == graphRev {
		return c.meta
	}
	meta := o.buildAnalyticsMeta()
	meta.Revision = c.meta.Revision + 1
	c.built, c.seq, c.graphRev, c.meta = true, seq, graphRev, meta
	return meta
}

func (o *Orchestrator) buildAnalyticsMeta() accounting.AnalyticsMeta {
	firstStart := map[string]time.Time{}
	history, _ := o.opts.Store.History()
	for _, tr := range history {
		if _, seen := firstStart[tr.StageID]; !seen && stageStartStatuses[tr.To] {
			firstStart[tr.StageID] = tr.Time
		}
	}

	// This process stays alive after the flow ended (the dashboard drains, the Assistant still answers), so the
	// jobs it opened keep running until their finished record.
	meta := accounting.AnalyticsMeta{LiveSince: o.liveSince}
	var participants []accounting.AgentMeta
	unrecorded := map[string]bool{}
	o.opts.Store.View(func(rs *state.RunState) {
		for id, st := range rs.Stages {
			unrecorded[id] = st.Timing.Incomplete
		}
		participants = participantsMeta(rs, o.SideAgentParticipantID())
	})
	for _, s := range o.reg().Stages() {
		meta.Stages = append(meta.Stages, accounting.StageMeta{ID: s.ID, Title: stageLabel(s), StartedAt: firstStart[s.ID], StartUnknown: unrecorded[s.ID]})
		meta.Agents = append(meta.Agents, accounting.AgentMeta{ID: string(meshStageParticipantID(s.ID)), Kind: accounting.AgentKindStage, Title: stageLabel(s)})
	}
	for _, a := range participants {
		a.Title = o.RedactSecrets(a.Title)
		meta.Agents = append(meta.Agents, a)
	}
	return meta
}

// participantsMeta lists every task by the mesh participant it runs as, and the Assistant, whether or not it
// has joined the mesh yet: usage that carries only a task ID or comes from the Assistant's ledger finds them.
func participantsMeta(rs *state.RunState, side agentcomm.ParticipantID) []accounting.AgentMeta {
	var out []accounting.AgentMeta
	for _, id := range slices.Sorted(maps.Keys(rs.Assistants.Tasks)) {
		t := rs.Assistants.Tasks[id]
		kind := accounting.AgentKindHelper
		if t.Scope == assistants.ScopeRun {
			kind = accounting.AgentKindRunAgent
		}
		title := t.Title
		if title == "" {
			title = string(t.ProfileID)
		}
		out = append(out, accounting.AgentMeta{ID: string(meshParticipantForTask(*rs, t.Task)), Kind: kind, Title: title, TaskID: string(id)})
	}
	return append(out, accounting.AgentMeta{ID: string(side), Kind: accounting.AgentKindAssistant, Title: "Assistant"})
}
