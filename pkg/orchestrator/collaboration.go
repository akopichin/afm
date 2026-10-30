package orchestrator

import "context"

// recoverCollaboration restores what a crash left in the assistants and mesh state. The order matters: each step
// reads what the previous one settled.
func (o *Orchestrator) recoverCollaboration(ctx context.Context) {
	o.settleAssistantsAfterRestart()
	o.recoverFeedbackIntents(ctx)
	o.recoverMeshWaits(ctx)
	o.recoverUnreadOfFinishedStages(ctx)
	// Closes what idle agents left once the turns a crash cut short are queued again; a turn started earlier
	// (a human's message, a stage) settles its own at its start (maybeScheduleTask).
	if o.startupReleaseHook != nil {
		o.startupReleaseHook()
	}
	o.releaseUnanswerableWaits(ctx, o.resumeWaiterAfterTerminalWaitAtStartup)
	o.resumeAssistantsAfterRestart(ctx)
	o.recoverMeshWaitDeadlines(ctx)
}

// startCollaborationLoops starts the background producers of the assistants feature. The watcher poller also
// serves a poisoned run held for inspection; the loop scheduler does not. stop is a no-op when nothing started.
func (o *Orchestrator) startCollaborationLoops(ctx context.Context, poisoned bool) (stop func()) {
	if !o.opts.AssistantsEnabled {
		return func() {}
	}
	o.startWatchPoller(ctx)
	if poisoned {
		return func() {}
	}
	// The scheduler is stopped and awaited before cancel and WaitAgents (LIFO defer): a tick must not outlive the
	// run or write to the journal after the Store closes.
	return o.startLoopScheduler(ctx)
}
