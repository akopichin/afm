package state

func AllStatuses() []StageStatus {
	return []StageStatus{
		StatusPending, StatusPlanning, StatusAwaitingApproval, StatusRevising,
		StatusReady, StatusRunning, StatusRetrying, StatusPaused, StatusAwaitingUserInput,
		StatusDone, StatusFailed, StatusHookFailed, StatusAwaitingAssistants, StatusAwaitingPeer,
		StatusAwaitingQuota,
	}
}
