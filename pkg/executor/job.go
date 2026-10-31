package executor

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/agentjobs"
)

// JobSink receives the boundary facts of agent jobs; *agentjobs.Store implements it.
type JobSink interface {
	Append(agentjobs.Record) error
}

// JobConfig makes every Run* call of an Executor a measured agent job. RunScript and RunJSONQuery are never jobs.
type JobConfig struct {
	// Meta is the job's identity; the executor assigns JobID per call.
	Meta agentjobs.JobMeta
	Sink JobSink
	// Segment places a job on the run's active timeline. It receives the start time the executor fixed;
	// nil means no placement.
	Segment func(startedAt time.Time) agentjobs.Segment
	// Yielded tells, for a turn that ended cleanly, whether it yielded into a mesh wait instead of answering.
	// It gets the turn's final text (a helper's answer; empty where the run keeps none); nil means never.
	Yielded func(finalText string) bool
}

// Reasons of a start_failed record: machine codes, never error text.
const (
	startFailedAgentStart  = "agent_start"
	startFailedCancelled   = "cancelled_before_start"
	startFailedInterrupted = "interrupted_before_start"
	startFailedSetup       = "setup"
)

const jobIDPrefix = "job-"

// jobRun is one Run* call's job. A nil *jobRun means jobs are off, so every method is a no-op on nil.
type jobRun struct {
	cfg *JobConfig
	now func() time.Time
	id  string

	started   bool
	closed    bool
	startedAt time.Time
	endedAt   time.Time
	// finalText is only handed to JobConfig.Yielded; it is never written anywhere.
	finalText string
}

// newJob gives the call its identity before anything can fail or start.
func (e *Executor) newJob() *jobRun {
	if e.cfg.Job == nil || e.cfg.Job.Sink == nil {
		return nil
	}
	return &jobRun{cfg: e.cfg.Job, now: e.clk().Now, id: jobIDPrefix + rand.Text()}
}

func (j *jobRun) jobID() string {
	if j == nil {
		return ""
	}
	return j.id
}

// markStarted fixes the start time right after cmd.Start succeeds and publishes it durably; the caller then
// hands the prompt to the process, so a started job is always on the journal before it can do any work.
func (j *jobRun) markStarted() {
	if j == nil {
		return
	}
	j.started = true
	j.startedAt = j.now()
	meta := j.cfg.Meta
	meta.JobID = j.id
	rec := agentjobs.Record{Type: agentjobs.TypeStarted, JobMeta: meta, At: j.startedAt}
	if j.cfg.Segment != nil {
		rec.Segment = j.cfg.Segment(j.startedAt)
	}
	j.publish(rec)
}

// markEnded fixes the end time once the process and its output are done, before any later disk write
// can stretch the measured duration.
func (j *jobRun) markEnded() {
	if j == nil || !j.started || !j.endedAt.IsZero() {
		return
	}
	j.endedAt = j.now()
}

// noteFinalText keeps the turn's final text for the Yielded decision.
func (j *jobRun) noteFinalText(text string) {
	if j != nil {
		j.finalText = text
	}
}

// close publishes the job's last fact exactly once: finished for a started process, start_failed otherwise.
func (j *jobRun) close(runErr error) {
	if j == nil || j.closed {
		return
	}
	j.closed = true
	if !j.started {
		meta := j.cfg.Meta
		meta.JobID = j.id
		j.publish(agentjobs.Record{Type: agentjobs.TypeStartFailed, JobMeta: meta, At: j.now(), Reason: startFailedReason(runErr)})
		return
	}
	if j.endedAt.IsZero() {
		j.endedAt = j.now()
	}
	outcome := jobOutcomeOf(runErr)
	if outcome == agentjobs.OutcomeOK && j.cfg.Yielded != nil && j.cfg.Yielded(j.finalText) {
		outcome = agentjobs.OutcomeYielded
	}
	j.publish(agentjobs.Record{
		Type:       agentjobs.TypeFinished,
		JobMeta:    agentjobs.JobMeta{JobID: j.id},
		At:         j.endedAt,
		Outcome:    outcome,
		DurationMS: max(j.endedAt.Sub(j.startedAt).Milliseconds(), 0),
	})
}

// abort closes the job for a failure before run() and returns the error unchanged.
func (j *jobRun) abort(err error) error {
	j.close(err)
	return err
}

// publish is best-effort: the journal latches itself unavailable on a write failure, and a
// broken journal must never fail the agent's turn.
func (j *jobRun) publish(rec agentjobs.Record) {
	_ = j.cfg.Sink.Append(rec)
}

// jobOutcomeOf maps the turn's final error (after usage-limit and terminal-result normalization) to a job outcome.
func jobOutcomeOf(runErr error) agentjobs.Outcome {
	var limit *UsageLimitError
	switch {
	case runErr == nil:
		return agentjobs.OutcomeOK
	case errors.As(runErr, &limit):
		return agentjobs.OutcomeUsageLimit
	case errors.Is(runErr, ErrUserInterrupted):
		return agentjobs.OutcomeInterrupted
	case errors.Is(runErr, ErrIdleTimeout):
		return agentjobs.OutcomeIdleTimeout
	case errors.Is(runErr, context.Canceled), errors.Is(runErr, context.DeadlineExceeded):
		return agentjobs.OutcomeCancelled
	default:
		return agentjobs.OutcomeError
	}
}

func startFailedReason(err error) string {
	switch {
	case errors.Is(err, ErrAgentStart):
		return startFailedAgentStart
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return startFailedCancelled
	case errors.Is(err, ErrUserInterrupted):
		return startFailedInterrupted
	default:
		return startFailedSetup
	}
}

// finishTurn is the synchronous end of every Run*: usage first, then the closed job, so a terminal
// transition that follows always sees both.
func (e *Executor) finishTurn(job *jobRun, collector *accounting.Collector, runErr error) {
	if e.cfg.OnUsage != nil {
		obs := collector.Finish(runErr)
		obs.JobID = job.jobID()
		e.cfg.OnUsage(obs)
	}
	job.close(runErr)
}
