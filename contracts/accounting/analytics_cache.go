package accounting

import (
	"slices"
	"sync"
	"sync/atomic"

	"github.com/akopichin/afm/contracts/agentjobs"
)

// JobsSource is the live journal; *agentjobs.Store satisfies it.
type JobsSource interface {
	Snapshot() agentjobs.Snapshot
}

// AnalyticsSources are the live inputs of one run. A nil Usage means accounting is off, a nil Jobs a run
// without a journal; Meta supplies stage and participant metadata and may be nil.
type AnalyticsSources struct {
	Usage *Store
	// UsageOpenFailed marks a nil Usage that is a failure, not a choice: usage.jsonl could not be opened.
	UsageOpenFailed bool
	Jobs            JobsSource
	Meta            func() AnalyticsMeta
	// Assistant is the side agent's own ledger and journal; its records join the facts as "outside the run total".
	Assistant *AssistantSources
}

// AssistantSources are the Assistant's inputs; either may be nil.
type AssistantSources struct {
	Usage *Store
	Jobs  JobsSource
}

// Analytics serves Facts for the live run. The facts are rebuilt only when usage, jobs or metadata changed,
// so polling an idle run reads no file and aggregates nothing.
type Analytics struct {
	src AnalyticsSources

	mu     sync.Mutex
	cached *Facts
	key    factsKey

	builds atomic.Int64 // test seam: how many times the facts were rebuilt
	// beforeAggregate runs after the inputs are copied and before they are aggregated.
	beforeAggregate func()
}

type factsKey struct {
	usageRev uint64
	usage    UsageState
	jobsRev  uint64
	metaRev  uint64
	// The Assistant revisions only grow, like the run's, so equal values mean unchanged inputs.
	sideUsageRev uint64
	sideJobsRev  uint64
}

func NewAnalytics(src AnalyticsSources) *Analytics { return &Analytics{src: src} }

// Facts returns the facts of the current revisions. Concurrent callers wait for one build instead of
// repeating it; writers never wait, because the build works on copies taken under a brief lock.
func (a *Analytics) Facts() *Facts {
	jobs := absentJobs()
	if a.src.Jobs != nil {
		jobs = a.src.Jobs.Snapshot()
	}
	var sideJobs agentjobs.Snapshot
	if side := a.src.Assistant; side != nil && side.Jobs != nil {
		sideJobs = side.Jobs.Snapshot()
	}
	var meta AnalyticsMeta
	if a.src.Meta != nil {
		meta = a.src.Meta()
	}
	usageRev, usageState := a.usageIdentity()
	sideUsageRev, _ := a.assistantUsageIdentity()

	a.mu.Lock()
	defer a.mu.Unlock()
	key := factsKey{usageRev, usageState, jobs.Revision, meta.Revision, sideUsageRev, sideJobs.Revision}
	if a.cached != nil && a.key == key {
		return a.cached
	}

	var records, sideRecords []UsageRecord
	if a.src.Usage != nil {
		records, usageRev, usageState = a.src.Usage.usageRecords()
	}
	if side := a.src.Assistant; side != nil && side.Usage != nil {
		sideRecords, sideUsageRev, _ = side.Usage.usageRecords()
	}
	runJobsRev := jobs.Revision
	jobs.Records = append(slices.Clone(jobs.Records), sideJobs.Records...)
	jobs.Revision += sideJobs.Revision
	if a.beforeAggregate != nil {
		a.beforeAggregate()
	}
	f := BuildFacts(AnalyticsInput{
		Usage: records, AssistantUsage: sideRecords, UsageRevision: usageRev + sideUsageRev, UsageState: usageState,
		Jobs: jobs, Meta: meta,
	})
	a.builds.Add(1)
	a.cached = f
	a.key = factsKey{usageRev, usageState, runJobsRev, meta.Revision, sideUsageRev, sideJobs.Revision}
	return f
}

func (a *Analytics) usageIdentity() (uint64, UsageState) {
	if a.src.Usage == nil {
		if a.src.UsageOpenFailed {
			return 0, UsageStateUnavailable
		}
		return 0, UsageStateDisabled
	}
	return a.src.Usage.usageIdentity()
}

func (a *Analytics) assistantUsageIdentity() (uint64, UsageState) {
	if a.src.Assistant == nil || a.src.Assistant.Usage == nil {
		return 0, UsageStateDisabled
	}
	return a.src.Assistant.Usage.usageIdentity()
}

// usageIdentity is the cheap cache check: the revision and state, without copying the ledger.
func (s *Store) usageIdentity() (uint64, UsageState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revision, s.usageState()
}

// usageRecords copies the ledger under the lock; the caller aggregates the copy outside it.
func (s *Store) usageRecords() ([]UsageRecord, uint64, UsageState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]UsageRecord(nil), s.ledger.recs...), s.revision, s.usageState()
}

// usageState must be called with mu held.
func (s *Store) usageState() UsageState {
	if s.unavailable {
		return UsageStateUnavailable
	}
	return UsageStateOK
}
