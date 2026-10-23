package state

import (
	"fmt"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

// Skipped slots and implicit ends are derived, not journaled.
// Loop effects retain only creation, delivered slots and explicit stops.

// LoopCreatedPayload omits the short ID and anchor so replay derives them from order and commit time.
type LoopCreatedPayload struct {
	CreatorID agentcomm.ParticipantID `json:"creator_id"`
	// TargetID is the participant resolved once at creation, so a later alias change cannot redirect ticks.
	TargetID agentcomm.ParticipantID `json:"target_id"`
	Interval time.Duration           `json:"interval"`
	// Prompt is stored already redacted.
	Prompt string `json:"prompt"`
	// UntilStageID, when set, ends the loop with that stage.
	UntilStageID string `json:"until_stage_id,omitempty"`
	// OriginStageID is the stage Feed the loop was created in; empty for a loop an agent created for the run.
	OriginStageID string `json:"origin_stage_id,omitempty"`
	// ThreadID is the mesh thread the loop's messages go to.
	ThreadID agentcomm.ThreadID `json:"thread_id"`
}

// LoopTickedPayload is the payload of loop_ticked: slot n is due at creation time + n × interval, counted from 1.
type LoopTickedPayload struct {
	Slot uint64 `json:"slot"`
}

// LoopState is one schedule as the projection holds it. Never change a stored LoopState in place: a changed
// copy replaces it (changeLoop), so snapshots taken earlier keep their own view.
type LoopState struct {
	Key string `json:"key"`
	// ShortID is the L<n> id a user types, allocated in creation order.
	ShortID       string                  `json:"short_id"`
	CreatorID     agentcomm.ParticipantID `json:"creator_id"`
	TargetID      agentcomm.ParticipantID `json:"target_id"`
	Interval      time.Duration           `json:"interval"`
	Prompt        string                  `json:"prompt"`
	UntilStageID  string                  `json:"until_stage_id,omitempty"`
	OriginStageID string                  `json:"origin_stage_id,omitempty"`
	ThreadID      agentcomm.ThreadID      `json:"thread_id"`
	// CreatedAt is the commit time of the create: the anchor every slot is counted from.
	CreatedAt  time.Time `json:"created_at"`
	CreatedSeq uint64    `json:"created_seq"`
	// LastSlot is the latest slot a tick was delivered for; 0 before the first tick.
	LastSlot uint64 `json:"last_slot,omitempty"`
	// StoppedBy/StoppedAt/StoppedSeq are set only by an explicit stop.
	StoppedBy  Actor     `json:"stopped_by,omitempty"`
	StoppedAt  time.Time `json:"stopped_at,omitempty"`
	StoppedSeq uint64    `json:"stopped_seq,omitempty"`
}

// Stopped reports whether the loop was stopped explicitly.
func (l LoopState) Stopped() bool { return l.StoppedSeq != 0 }

// SlotTime is when slot n (counted from 1) is due.
func (l LoopState) SlotTime(n uint64) time.Time {
	return l.CreatedAt.Add(time.Duration(n) * l.Interval)
}

// LoopByShortID returns the loop with the given L<n> id.
func (as AssistantsState) LoopByShortID(shortID string) (LoopState, bool) {
	for _, l := range as.OrderedLoops() {
		if l.ShortID == shortID {
			return l, true
		}
	}
	return LoopState{}, false
}

// OrderedLoops returns every loop, stopped ones too, in creation order.
func (as AssistantsState) OrderedLoops() []LoopState {
	out := make([]LoopState, 0, len(as.LoopOrder))
	for _, key := range as.LoopOrder {
		out = append(out, *as.Loops[key])
	}
	return out
}

func loopRejected(code assistants.ErrorCode, format string, args ...any) error {
	return &OperationRejectedError{Code: code, Reason: fmt.Sprintf(format, args...)}
}

func applyLoopCreated(as *AssistantsState, c *Control, dryRun bool) error {
	key := c.Target.LoopKey
	var p LoopCreatedPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode %s payload: %w", c.Kind, err)
	}
	switch {
	case key == "":
		return loopRejected(assistants.CodeInvalidRequest, "%s: empty target loop_key", c.Kind)
	case p.CreatorID == "" || p.TargetID == "":
		return loopRejected(assistants.CodeInvalidRequest, "loop %s needs a creator and a target", key)
	case p.Interval <= 0:
		return loopRejected(assistants.CodeInvalidRequest, "loop %s needs a positive interval", key)
	case p.Prompt == "":
		return loopRejected(assistants.CodeInvalidRequest, "loop %s needs a prompt", key)
	case p.ThreadID == "":
		return loopRejected(assistants.CodeInvalidRequest, "loop %s needs a thread id", key)
	}
	if _, exists := as.Loops[key]; exists {
		return loopRejected(assistants.CodeInvalidStatus, "loop %s already exists", key)
	}
	if dryRun {
		return nil
	}

	if as.Loops == nil {
		as.Loops = map[string]*LoopState{}
	}
	as.Loops[key] = &LoopState{
		Key:           key,
		ShortID:       fmt.Sprintf("L%d", len(as.LoopOrder)+1),
		CreatorID:     p.CreatorID,
		TargetID:      p.TargetID,
		Interval:      p.Interval,
		Prompt:        p.Prompt,
		UntilStageID:  p.UntilStageID,
		OriginStageID: p.OriginStageID,
		ThreadID:      p.ThreadID,
		CreatedAt:     c.Time,
		CreatedSeq:    c.Seq,
	}
	as.LoopOrder = append(as.LoopOrder, key)
	return nil
}

func applyLoopTicked(as *AssistantsState, c *Control, dryRun bool) error {
	var p LoopTickedPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode %s payload: %w", c.Kind, err)
	}
	if p.Slot == 0 {
		return loopRejected(assistants.CodeInvalidRequest, "loop %s: slots are counted from 1", c.Target.LoopKey)
	}
	return changeLoop(as, c, dryRun, func(l *LoopState) error {
		if p.Slot <= l.LastSlot {
			return loopRejected(assistants.CodeInvalidStatus, "loop %s: slot %d is not after the last delivered slot %d", l.ShortID, p.Slot, l.LastSlot)
		}
		l.LastSlot = p.Slot
		return nil
	})
}

func applyLoopStopped(as *AssistantsState, c *Control, dryRun bool) error {
	return changeLoop(as, c, dryRun, func(l *LoopState) error {
		l.StoppedBy, l.StoppedAt, l.StoppedSeq = c.Actor, c.Time, c.Seq
		return nil
	})
}

// changeLoop applies change to a copy of the loop c names and stores the copy. A loop that was stopped
// explicitly takes no further change.
func changeLoop(as *AssistantsState, c *Control, dryRun bool, change func(l *LoopState) error) error {
	key := c.Target.LoopKey
	if key == "" {
		return loopRejected(assistants.CodeInvalidRequest, "%s: empty target loop_key", c.Kind)
	}
	current, ok := as.Loops[key]
	if !ok {
		return loopRejected(assistants.CodeNotFound, "loop %s does not exist", key)
	}
	if current.Stopped() {
		return loopRejected(assistants.CodeInvalidStatus, "loop %s is already stopped", current.ShortID)
	}
	updated := *current
	if err := change(&updated); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	as.Loops[key] = &updated
	return nil
}
