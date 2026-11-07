package accounting

import (
	"slices"
	"time"

	"github.com/akopichin/afm/contracts/agentjobs"
)

// StageSnapshot is the cumulative time and token picture of one stage at the moment of a terminal
// transition, for the lifecycle hook. It carries no money by construction.
type StageSnapshot struct {
	AsOf          time.Time
	UsageRevision uint64
	JobsRevision  uint64
	Jobs          StageJobs
	Usage         StageUsage
}

// StageJobs counts the AI jobs of the stage and its helper descendants. Known is false when the journal
// cannot give counts at all (legacy run, unreadable journal); Complete is true only for a picture that
// nothing running, interrupted or unrecorded can still change.
type StageJobs struct {
	Known       bool
	Count       int
	Active      int
	Measured    int
	UnknownTime int
	DurationMS  int64
	Complete    bool
	Reasons     []string
}

// StageUsage sums the tokens of the stage's finished jobs. Tokens is nil when the figure is unknown; an
// honest zero is a non-nil value with no counts.
type StageUsage struct {
	Health    Health
	Coverage  Coverage
	Metered   int
	Unmetered int
	Pending   int
	Tokens    *Tokens
	Reasons   []string
}

// StageSnapshot freezes the stage as of at: later jobs are out, and a job not yet ended by then is active
// with its usage left out, even if the journal knows its end. pendingStart flags unpublished helper work.
func (f *Facts) StageSnapshot(stageID string, at time.Time, pendingStart bool) StageSnapshot {
	snap := StageSnapshot{AsOf: at, UsageRevision: f.usageRev, JobsRevision: f.jobsRev}
	jobGaps := f.coverageGaps(f.stageByID[stageID])

	var tokens Tokens
	var counted int
	for i := range f.entries {
		e := &f.entries[i]
		// Usage of a job the frozen journal does not have belongs to work published after it; only legacy
		// usage without a job ID is taken by its fields.
		if e.stageID != stageID || e.unjoined {
			continue
		}
		active := false
		if e.kind == entryJob {
			if e.at.After(at) {
				continue
			}
			snap.Jobs.Count++
			switch {
			case !e.endedAt.IsZero() && !e.endedAt.After(at):
				snap.Jobs.Measured++
				snap.Jobs.DurationMS += *e.durationMS
			case e.running || !e.endedAt.IsZero():
				snap.Jobs.Active++
				active = true
			default:
				snap.Jobs.UnknownTime++
			}
		}
		if active {
			snap.Usage.Pending++
			continue
		}
		switch e.tokenState() {
		case tokensMetered:
			snap.Usage.Metered++
			tokens = tokens.Add(e.usage.tokens)
			counted++
		case tokensUnmetered:
			snap.Usage.Unmetered++
			counted++
		default: // pending and none add nothing to a frozen picture
		}
	}

	snap.Jobs.Reasons = slices.Clone(jobGaps)
	if snap.Jobs.Active > 0 {
		snap.Jobs.Reasons = append(snap.Jobs.Reasons, ReasonActiveJobs)
	}
	if snap.Jobs.UnknownTime > 0 {
		snap.Jobs.Reasons = append(snap.Jobs.Reasons, ReasonInterruptedJob)
	}
	if pendingStart {
		snap.Jobs.Reasons = append(snap.Jobs.Reasons, ReasonPendingStart)
	}
	snap.Jobs.Known = !slices.Contains(jobGaps, agentjobs.GapLegacyJournal) && !slices.Contains(jobGaps, agentjobs.GapJobsStoreUnavailable)
	snap.Jobs.Complete = snap.Jobs.Known && len(snap.Jobs.Reasons) == 0

	f.fillStageUsage(&snap.Usage, tokens, counted, jobGaps, pendingStart)
	return snap
}

// coverageGaps lists why the stage's zero jobs or tokens are not proven. A stage whose start is unknown ran
// before the journal recorded it, like one that started before recording_started_at.
func (f *Facts) coverageGaps(st StageMeta) []string {
	gaps := agentjobs.CoverageGaps(f.journal, st.StartedAt, true)
	if st.StartUnknown && !f.legacyJournal() && !slices.Contains(gaps, agentjobs.GapStartedBeforeRecording) {
		gaps = append(gaps, agentjobs.GapStartedBeforeRecording)
	}
	return gaps
}

// fillStageUsage decides what the usage side may claim. Zero finished entries is a proven zero only when
// the journal can vouch that the stage started no job and none is about to start.
func (f *Facts) fillStageUsage(u *StageUsage, tokens Tokens, counted int, jobGaps []string, pendingStart bool) {
	u.Health = HealthOK
	if reasons := f.usageReasons(); len(reasons) > 0 {
		u.Health, u.Coverage, u.Reasons = HealthUnavailable, CoverageNone, reasons
		return
	}
	if u.Unmetered > 0 {
		u.Reasons = append(u.Reasons, ReasonUnmeteredJob)
	}
	if u.Pending > 0 {
		u.Reasons = append(u.Reasons, ReasonPendingUsage)
	}
	if pendingStart {
		u.Reasons = append(u.Reasons, ReasonPendingStart)
	}
	waiting := u.Pending > 0 || pendingStart
	switch {
	case counted == 0 && !waiting && len(jobGaps) > 0:
		u.Coverage, u.Reasons = CoverageNone, slices.Clone(jobGaps)
		return
	case counted == 0 && waiting:
		u.Coverage = CoverageNone
	case u.Metered > 0 && u.Unmetered == 0 && !waiting:
		u.Coverage = CoverageFull
	case u.Metered > 0:
		u.Coverage = CoveragePartial
	case counted == 0:
		u.Coverage = CoverageFull
	default:
		u.Coverage = CoverageNone
	}
	u.Tokens = &tokens
}
