package runreport

import (
	"maps"
	"slices"
	"strings"

	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/agentcomm"
	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/state"
)

// OfflineMeta rebuilds what the live orchestrator hands to the analytics (afm-engine
// internal/orchestrator/agent_jobs.go, buildAnalyticsMeta) from the replayed state alone. LiveSince stays
// zero: nothing is alive in an offline read.
func OfflineMeta(src *Sources) accounting.AnalyticsMeta {
	rs := src.State
	meta := accounting.AnalyticsMeta{}
	for _, id := range rs.StageOrder {
		meta.Stages = append(meta.Stages, accounting.StageMeta{
			ID: id, Title: id, StartedAt: src.FirstStart[id], StartUnknown: rs.Stages[id].Timing.Incomplete,
		})
		meta.Agents = append(meta.Agents, accounting.AgentMeta{ID: stageParticipantPrefix + id, Kind: accounting.AgentKindStage, Title: id})
	}
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
		meta.Agents = append(meta.Agents, accounting.AgentMeta{
			ID: string(participantForTask(rs, t.Task)), Kind: kind, Title: title, TaskID: string(id),
		})
	}
	meta.Agents = append(meta.Agents, accounting.AgentMeta{
		ID: assistantParticipantID(rs, src.RunID), Kind: accounting.AgentKindAssistant, Title: "Assistant",
	})
	return meta
}

const (
	stageParticipantPrefix = "stage:"
	runParticipantPrefix   = "run:"
	sideParticipantPrefix  = "side:"
)

// participantForTask is the mesh identity a task runs as: a copy of afm-engine
// internal/orchestrator/agentcomm_roster.go meshParticipantForTask, whose legacy owner lookup is runTaskOwner.
func participantForTask(rs state.RunState, task assistants.Task) agentcomm.ParticipantID {
	if task.Scope != assistants.ScopeRun {
		return agentcomm.HelperParticipantID(task.ID)
	}
	own := agentcomm.ParticipantID(runParticipantPrefix + string(task.ID))
	if _, ok := rs.Assistants.Mesh.Participants[own]; ok {
		return own
	}
	legacy := agentcomm.ParticipantID(runParticipantPrefix + string(task.ProfileID))
	if owner, ok := runTaskOwner(rs, legacy); ok && owner == task.ID {
		return legacy
	}
	return own
}

// runTaskOwner maps a registered legacy "run:<profileID>" participant to the lowest-ID run task of that profile
// that has no "run:<taskID>" participant of its own (afm-engine runTaskForParticipant, legacy branch).
func runTaskOwner(rs state.RunState, id agentcomm.ParticipantID) (assistants.TaskID, bool) {
	suffix, ok := strings.CutPrefix(string(id), runParticipantPrefix)
	if !ok || suffix == "" {
		return "", false
	}
	if _, registered := rs.Assistants.Mesh.Participants[id]; !registered {
		return "", false
	}
	if t, isTask := rs.Assistants.Tasks[assistants.TaskID(suffix)]; isTask && t.Scope == assistants.ScopeRun {
		return assistants.TaskID(suffix), true
	}
	var owner assistants.TaskID
	for tid, t := range rs.Assistants.Tasks {
		if t.Scope != assistants.ScopeRun || string(t.ProfileID) != suffix {
			continue
		}
		own := agentcomm.ParticipantID(runParticipantPrefix + string(tid))
		if _, hasOwn := rs.Assistants.Mesh.Participants[own]; hasOwn {
			continue
		}
		if owner == "" || tid < owner {
			owner = tid
		}
	}
	return owner, owner != ""
}

// assistantParticipantID is the registered side-agent participant; a run that never registered one still has
// the identity the engine would give it, so the Assistant's usage finds its agent.
func assistantParticipantID(rs state.RunState, runID string) string {
	for id, p := range rs.Assistants.Mesh.Participants {
		if p.Kind == agentcomm.KindSideAgent {
			return string(id)
		}
	}
	return sideParticipantPrefix + runID
}

// buildFacts aggregates the sources into the facts the dashboard would show for a finished run. The
// Assistant's records join the run's the way contracts/accounting/analytics_cache.go Analytics.Facts joins
// them: records are appended, the header and health stay the run's.
func buildFacts(src *Sources) *accounting.Facts {
	jobs := src.Jobs.Snapshot
	jobs.Records = append(slices.Clone(jobs.Records), src.Assistant.Journal.Snapshot.Records...)
	jobs.Revision += src.Assistant.Journal.Snapshot.Revision

	usageState := accounting.UsageStateOK
	if src.Usage.Err != nil {
		usageState = accounting.UsageStateUnavailable
	}
	return accounting.BuildFacts(accounting.AnalyticsInput{
		Usage:          src.Usage.Records,
		AssistantUsage: src.Assistant.Usage.Records,
		UsageState:     usageState,
		Jobs:           jobs,
		Meta:           OfflineMeta(src),
	})
}
