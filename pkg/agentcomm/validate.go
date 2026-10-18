package agentcomm

import (
	"errors"
	"fmt"
)

// Sentinel errors for ValidateMessage's structural rejections. Each is returned unwrapped (or
// wrapped with %w, preserving errors.Is) so a caller can distinguish "which rule failed" and
// report an explicit rejection — never silently truncate, guess a default, or collapse every
// failure into one generic error.
var (
	ErrUnknownMessageKind       = errors.New("agentcomm: unknown message kind")
	ErrEmptyRecipients          = errors.New("agentcomm: message has no recipients")
	ErrTooManyRecipients        = errors.New("agentcomm: too many recipients")
	ErrDuplicateRecipient       = errors.New("agentcomm: duplicate recipient")
	ErrSenderInRecipients       = errors.New("agentcomm: sender cannot be its own recipient")
	ErrRecipientNotThreadMember = errors.New("agentcomm: recipient is not a member of the thread")
	ErrSenderNotThreadMember    = errors.New("agentcomm: sender is not a member of the thread")
	ErrReplyMissingInReplyTo    = errors.New("agentcomm: reply message is missing in_reply_to")
	ErrInReplyToUnknown         = errors.New("agentcomm: in_reply_to does not name a message in this thread")
	ErrBodyTooLarge             = errors.New("agentcomm: message body exceeds MaxBodyBytes")
	ErrTooManyArtifactRefs      = errors.New("agentcomm: too many artifact refs")
)

// ValidateMessage enforces every structural rule a Message must satisfy before it can be
// admitted to a thread. It deliberately does not know about addressing/reachability
// (CanAddress) or thread-level policy beyond membership (open/closed, who may add members) —
// those need the full roster and thread state, which is the orchestrator's job once it has a
// Store to read from.
//
// threadMemberIDs is the thread's fixed member set; when nil or empty, the thread-membership
// check is skipped (useful for validating a message's own shape before a thread exists yet, e.g.
// OpenThread's first message). knownMessageIDsInThread is the set of message ids already present
// in the thread, used to validate InReplyTo. Both are supplied by the caller rather than looked
// up here, keeping this function pure and testable without a store.
func ValidateMessage(msg Message, threadMemberIDs []ParticipantID, knownMessageIDsInThread map[MessageID]bool) error {
	if !msg.Kind.Valid() {
		return fmt.Errorf("%w: %q", ErrUnknownMessageKind, msg.Kind)
	}
	if len(msg.Recipients) == 0 {
		return ErrEmptyRecipients
	}
	if len(msg.Recipients) > MaxRecipients {
		return fmt.Errorf("%w: %d > %d", ErrTooManyRecipients, len(msg.Recipients), MaxRecipients)
	}

	members := make(map[ParticipantID]bool, len(threadMemberIDs))
	for _, id := range threadMemberIDs {
		members[id] = true
	}
	if len(members) > 0 && !members[msg.Sender] {
		return fmt.Errorf("%w: %q", ErrSenderNotThreadMember, msg.Sender)
	}

	seen := make(map[ParticipantID]bool, len(msg.Recipients))
	for _, r := range msg.Recipients {
		if r == msg.Sender {
			return fmt.Errorf("%w: %q", ErrSenderInRecipients, r)
		}
		if seen[r] {
			return fmt.Errorf("%w: %q", ErrDuplicateRecipient, r)
		}
		seen[r] = true
		if len(members) > 0 && !members[r] {
			return fmt.Errorf("%w: %q", ErrRecipientNotThreadMember, r)
		}
	}

	if msg.Kind == MessageKindReply && msg.InReplyTo == nil {
		return ErrReplyMissingInReplyTo
	}
	if msg.InReplyTo != nil && !knownMessageIDsInThread[*msg.InReplyTo] {
		return fmt.Errorf("%w: %q", ErrInReplyToUnknown, *msg.InReplyTo)
	}

	if len(msg.Body) > MaxBodyBytes {
		return fmt.Errorf("%w: %d > %d", ErrBodyTooLarge, len(msg.Body), MaxBodyBytes)
	}
	if len(msg.ArtifactRefs) > MaxArtifactRefs {
		return fmt.Errorf("%w: %d > %d", ErrTooManyArtifactRefs, len(msg.ArtifactRefs), MaxArtifactRefs)
	}

	return nil
}
