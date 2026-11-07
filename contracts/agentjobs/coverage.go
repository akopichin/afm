package agentjobs

import (
	"slices"
	"time"
)

// Machine reasons why "this stage ran no agent jobs" cannot be claimed as a fact.
const (
	GapLegacyJournal          = "legacy_journal"
	GapStartedBeforeRecording = "started_before_recording"
	GapJobsStoreUnavailable   = "jobs_store_unavailable"
	GapUsageStoreUnavailable  = "usage_store_unavailable"
)

// CoverageGaps lists why zero jobs or tokens for a stage are not proven; empty means proven.
// A zero stageStart means the stage never started. Anything short of a healthy journal counts
// as a gap, because a skipped line could be the stage's job.
func CoverageGaps(snap Snapshot, stageStart time.Time, usageHealthy bool) []string {
	var gaps []string
	legacy := slices.Contains(snap.Health.Reasons, ReasonNoJournal)
	if legacy {
		gaps = append(gaps, GapLegacyJournal)
	} else if snap.Health.Status != HealthOK {
		gaps = append(gaps, GapJobsStoreUnavailable)
	}
	if !legacy && !stageStart.IsZero() && stageStart.Before(snap.Header.RecordingStartedAt) {
		gaps = append(gaps, GapStartedBeforeRecording)
	}
	if !usageHealthy {
		gaps = append(gaps, GapUsageStoreUnavailable)
	}
	return gaps
}
