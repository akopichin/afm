package agentcomm

// Limits bound every unbounded-looking dimension of the mesh, so a run can never grow a thread,
// an inbox, or a wait graph without bound. Each is enforced explicitly where it applies — see
// ValidateMessage for the message-level limits, and the pkg/state/pkg/orchestrator phases for the
// rest.
const (
	// MaxBodyBytes is the largest a single Message.Body may be.
	MaxBodyBytes = 16 * 1024
	// MaxRecipients is the most explicit recipients a single Message may name.
	MaxRecipients = 8
	// MaxThreadMembers is the most agents a single Thread may fix at open time; the human, who joins every
	// conversation, does not count.
	MaxThreadMembers = 8
	// MaxArtifactRefs is the most artifact references a single Message or BoardEntry may carry.
	MaxArtifactRefs = 8
	// MaxInboxUnread is the most messages a single participant may have waiting to be handed to it
	// (accepted, not yet delivered) before new messages are rejected rather than queued without bound.
	MaxInboxUnread = 200
	// MaxInboxPassive is the same bound for passive turn results (Message.AutoResult), counted separately: they
	// wait for the recipient's next turn without waking it, so they must never use up the room a waking
	// message needs.
	MaxInboxPassive = 200
	// MaxHumanInbox is how many of the newest messages to the human its inbox index keeps. The human is not
	// limited by MaxInboxUnread (the dashboard shows every thread in full, nothing is handed over), so the
	// index drops its oldest entries instead; those messages stay in their threads.
	MaxHumanInbox = 1000
	// MaxThreadMessages is the most messages a single Thread may ever hold — Thread.Budgets'
	// effective cap when ThreadBudgets.MaxMessages is left zero.
	MaxThreadMessages = 200
	// MaxBoardNotify is the most participants one board publish may name to notify. Each notice is a message
	// committed in the publish's own operation, so this keeps that one journal record well below the size a
	// retry reads back.
	MaxBoardNotify = 16
	// MaxCausalHops bounds how many reply/wait links the cycle check (pkg/orchestrator) will walk
	// before giving up, so a malformed or adversarial chain cannot hang admission.
	MaxCausalHops = 12
)
