package state

import (
	"errors"
	"fmt"
)

// FeedbackIntentCreatedPayload is the feedback_intent_created control record's payload — decoded here by
// applyFeedbackIntentCreated, encoded by pkg/orchestrator's feedbackIntentCreatedEffect (T6.5/T6.6).
type FeedbackIntentCreatedPayload struct {
	StageID                 string   `json:"stage_id"`
	Feedback                string   `json:"feedback"`
	Origin                  string   `json:"origin"`
	ProposalID              string   `json:"proposal_id,omitempty"`
	Participants            []string `json:"participants,omitempty"`
	ExpectedStageGeneration uint64   `json:"expected_stage_generation"`
}

// FeedbackIntentConsumedPayload is the feedback_intent_consumed control record's payload — the terminal
// outcome of one delivery attempt (delivered, or rejected with a reason — e.g. stale generation, stage
// reached done/failed).
type FeedbackIntentConsumedPayload struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// ErrUnknownFeedbackIntent — a control record addresses an intent id that does not exist in the projection.
var ErrUnknownFeedbackIntent = errors.New("assistants: unknown feedback_intent id")

// ErrFeedbackIntentAlreadyConsumed — a feedback_intent_consumed record targets an intent that is no longer
// "accepted" (already consumed once) — delivery must happen EXACTLY once (T6.6).
var ErrFeedbackIntentAlreadyConsumed = errors.New("assistants: feedback_intent already consumed")

func applyFeedbackIntentCreated(as *AssistantsState, c *Control, dryRun bool) error {
	var p FeedbackIntentCreatedPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode feedback_intent_created payload: %w", err)
	}
	id := c.Target.IntentID
	if id == "" {
		return errors.New("feedback_intent_created: empty target intent_id")
	}
	if p.StageID == "" {
		return errors.New("feedback_intent_created: empty stage_id")
	}
	if _, exists := as.FeedbackIntents[id]; exists {
		return fmt.Errorf("feedback_intent_created: id %s already exists", id)
	}
	if dryRun {
		return nil
	}
	as.FeedbackIntents[id] = &FeedbackIntent{
		ID:                      id,
		StageID:                 p.StageID,
		Feedback:                p.Feedback,
		Origin:                  FeedbackIntentOrigin(p.Origin),
		ProposalID:              p.ProposalID,
		Participants:            p.Participants,
		ExpectedStageGeneration: p.ExpectedStageGeneration,
		Status:                  FeedbackIntentAccepted,
		CreatedAt:               c.Time,
	}
	return nil
}

func applyFeedbackIntentConsumed(as *AssistantsState, c *Control, dryRun bool) error {
	id := c.Target.IntentID
	fi, ok := as.FeedbackIntents[id]
	if !ok {
		return fmt.Errorf("feedback_intent %s: %w", id, ErrUnknownFeedbackIntent)
	}
	var p FeedbackIntentConsumedPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("feedback_intent %s: decode feedback_intent_consumed payload: %w", id, err)
	}
	if fi.Status != FeedbackIntentAccepted {
		return fmt.Errorf("feedback_intent %s: %w (current status %s)", id, ErrFeedbackIntentAlreadyConsumed, fi.Status)
	}
	if dryRun {
		return nil
	}
	fi.Status = FeedbackIntentStatus(p.Status)
	fi.Reason = p.Reason
	fi.ConsumedAt = c.Time
	return nil
}
