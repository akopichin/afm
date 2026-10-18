package agentcomm

import "github.com/akopichin/afm/pkg/assistants"

// ErrorCode is a type alias (not a new type) for assistants.ErrorCode: the two packages share one
// stable, machine-readable error vocabulary rather than keeping parallel catalogs that an HTTP
// layer would have to map twice. See pkg/assistants/errors.go for the existing codes
// (feature_disabled, budget_exceeded, stale_revision, ...), none of which fit the four new
// situations below closely enough to reuse as-is.
type ErrorCode = assistants.ErrorCode

const (
	// CodeRecipientUnreachable — a Send named a recipient that CanAddress/the roster allows in
	// principle, but who cannot currently receive anything (terminal stage/task, or an executor
	// without mesh capability). Distinct from the structural ValidateMessage rejections in
	// validate.go, which never even reach roster/reachability data.
	CodeRecipientUnreachable ErrorCode = "recipient_unreachable"
	// CodeThreadClosed — an operation (Send, Wait, ...) targets a Thread whose Status is
	// ThreadClosed. CloseThread resolves any waits depending on the thread with this same code,
	// never passing closure off as an answer (agent-mesh.md §5.4).
	CodeThreadClosed ErrorCode = "thread_closed"
	// CodeWaitCycle — registering a Wait would complete a closed cycle in which no participant
	// could make the next move (A waits on B, B waits on A; a parent waiting on a child that is
	// itself waiting on that parent; ...). A serviceable counter-question is not a cycle.
	CodeWaitCycle ErrorCode = "wait_cycle"
	// CodeMessagingUnsupported — the participant's executor/adapter does not have a verified
	// send/read/reply/yield-resume path (agent-mesh.md §6.2.2's capability matrix). Returned
	// instead of silently degrading to "message delivered" when it was not.
	CodeMessagingUnsupported ErrorCode = "messaging_unsupported"
)
