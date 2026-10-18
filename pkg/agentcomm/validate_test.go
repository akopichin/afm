package agentcomm

import (
	"errors"
	"strings"
	"testing"
)

// baseMessage returns a message that passes every ValidateMessage rule, so each test below only
// has to break exactly one rule.
func baseMessage() Message {
	return Message{
		ID:         "m1",
		ThreadID:   "t1",
		Sender:     "user",
		Recipients: []ParticipantID{"stage:build"},
		Kind:       MessageKindMessage,
		Body:       "status?",
	}
}

var noThreadMembers []ParticipantID
var noKnownMessages map[MessageID]bool

func TestValidateMessage_AcceptsValidMessage(t *testing.T) {
	if err := ValidateMessage(baseMessage(), noThreadMembers, noKnownMessages); err != nil {
		t.Fatalf("ValidateMessage(valid) = %v, want nil", err)
	}
}

func TestValidateMessage_EmptyRecipientsRejected(t *testing.T) {
	msg := baseMessage()
	msg.Recipients = nil
	err := ValidateMessage(msg, noThreadMembers, noKnownMessages)
	if !errors.Is(err, ErrEmptyRecipients) {
		t.Fatalf("ValidateMessage(empty recipients) = %v, want ErrEmptyRecipients", err)
	}
}

func TestValidateMessage_DuplicateRecipientRejected(t *testing.T) {
	msg := baseMessage()
	msg.Recipients = []ParticipantID{"stage:build", "stage:build"}
	err := ValidateMessage(msg, noThreadMembers, noKnownMessages)
	if !errors.Is(err, ErrDuplicateRecipient) {
		t.Fatalf("ValidateMessage(duplicate recipient) = %v, want ErrDuplicateRecipient", err)
	}
}

func TestValidateMessage_SenderInRecipientsRejected(t *testing.T) {
	msg := baseMessage()
	msg.Sender = "stage:build"
	msg.Recipients = []ParticipantID{"stage:build"}
	err := ValidateMessage(msg, noThreadMembers, noKnownMessages)
	if !errors.Is(err, ErrSenderInRecipients) {
		t.Fatalf("ValidateMessage(sender in recipients) = %v, want ErrSenderInRecipients", err)
	}
}

func TestValidateMessage_ReplyWithoutInReplyToRejected(t *testing.T) {
	msg := baseMessage()
	msg.Kind = MessageKindReply
	msg.InReplyTo = nil
	err := ValidateMessage(msg, noThreadMembers, noKnownMessages)
	if !errors.Is(err, ErrReplyMissingInReplyTo) {
		t.Fatalf("ValidateMessage(reply without in_reply_to) = %v, want ErrReplyMissingInReplyTo", err)
	}
}

func TestValidateMessage_InReplyToOutsideThreadRejected(t *testing.T) {
	msg := baseMessage()
	msg.Kind = MessageKindReply
	outside := MessageID("not-in-thread")
	msg.InReplyTo = &outside
	known := map[MessageID]bool{"m0": true}
	err := ValidateMessage(msg, noThreadMembers, known)
	if !errors.Is(err, ErrInReplyToUnknown) {
		t.Fatalf("ValidateMessage(in_reply_to outside thread) = %v, want ErrInReplyToUnknown", err)
	}
}

func TestValidateMessage_InReplyToKnownAccepted(t *testing.T) {
	msg := baseMessage()
	msg.Kind = MessageKindReply
	known0 := MessageID("m0")
	msg.InReplyTo = &known0
	known := map[MessageID]bool{"m0": true}
	if err := ValidateMessage(msg, noThreadMembers, known); err != nil {
		t.Fatalf("ValidateMessage(in_reply_to known) = %v, want nil", err)
	}
}

func TestValidateMessage_BodyTooLargeRejected(t *testing.T) {
	msg := baseMessage()
	msg.Body = strings.Repeat("x", MaxBodyBytes+1)
	err := ValidateMessage(msg, noThreadMembers, noKnownMessages)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("ValidateMessage(oversized body) = %v, want ErrBodyTooLarge", err)
	}
}

func TestValidateMessage_TooManyRecipientsRejected(t *testing.T) {
	msg := baseMessage()
	recipients := make([]ParticipantID, 0, MaxRecipients+1)
	for i := 0; i < MaxRecipients+1; i++ {
		recipients = append(recipients, ParticipantID(strings.Repeat("r", 1)+string(rune('a'+i))))
	}
	msg.Recipients = recipients
	err := ValidateMessage(msg, noThreadMembers, noKnownMessages)
	if !errors.Is(err, ErrTooManyRecipients) {
		t.Fatalf("ValidateMessage(too many recipients) = %v, want ErrTooManyRecipients", err)
	}
}

func TestValidateMessage_TooManyArtifactRefsRejected(t *testing.T) {
	msg := baseMessage()
	refs := make([]string, 0, MaxArtifactRefs+1)
	for i := 0; i < MaxArtifactRefs+1; i++ {
		refs = append(refs, "artifacts/f"+string(rune('a'+i)))
	}
	msg.ArtifactRefs = refs
	err := ValidateMessage(msg, noThreadMembers, noKnownMessages)
	if !errors.Is(err, ErrTooManyArtifactRefs) {
		t.Fatalf("ValidateMessage(too many artifact refs) = %v, want ErrTooManyArtifactRefs", err)
	}
}

func TestValidateMessage_UnknownKindRejected(t *testing.T) {
	msg := baseMessage()
	msg.Kind = MessageKind("bogus")
	err := ValidateMessage(msg, noThreadMembers, noKnownMessages)
	if !errors.Is(err, ErrUnknownMessageKind) {
		t.Fatalf("ValidateMessage(unknown kind) = %v, want ErrUnknownMessageKind", err)
	}
}

func TestValidateMessage_RecipientNotThreadMemberRejected(t *testing.T) {
	msg := baseMessage()
	msg.Recipients = []ParticipantID{"helper:not-a-member"}
	members := []ParticipantID{"user", "stage:build"}
	err := ValidateMessage(msg, members, noKnownMessages)
	if !errors.Is(err, ErrRecipientNotThreadMember) {
		t.Fatalf("ValidateMessage(recipient not thread member) = %v, want ErrRecipientNotThreadMember", err)
	}
}

func TestValidateMessage_RecipientThreadMemberAccepted(t *testing.T) {
	msg := baseMessage()
	members := []ParticipantID{"user", "stage:build"}
	if err := ValidateMessage(msg, members, noKnownMessages); err != nil {
		t.Fatalf("ValidateMessage(recipient is thread member) = %v, want nil", err)
	}
}

// TestValidateMessage_EachRejectionIsDistinguishable proves every rejection above maps to its
// own, distinguishable sentinel error rather than a single generic "invalid message" error a
// caller could not tell apart (dev-plan.md T0.2's explicit-rejection requirement).
func TestValidateMessage_EachRejectionIsDistinguishable(t *testing.T) {
	sentinels := []error{
		ErrUnknownMessageKind,
		ErrEmptyRecipients,
		ErrTooManyRecipients,
		ErrDuplicateRecipient,
		ErrSenderInRecipients,
		ErrRecipientNotThreadMember,
		ErrReplyMissingInReplyTo,
		ErrInReplyToUnknown,
		ErrBodyTooLarge,
		ErrTooManyArtifactRefs,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i == j {
				continue
			}
			if errors.Is(a, b) {
				t.Fatalf("sentinel %v must not satisfy errors.Is against %v", a, b)
			}
		}
	}
}
