package state

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/assistants"
)

// ErrPolicyNotRecorded means an operation that creates a task was attempted before any policy_set record
// exists for this run. ApplyRunOperation enforces this for any operation bundling a task_created effect;
// production code (cmd/afm, T2.7) calls EnsurePolicyRecorded during recovery, before any consumer can start
// admitting task-creation operations, so this should be unreachable outside of a sequencing bug.
var ErrPolicyNotRecorded = errors.New("assistants: no policy recorded yet for this run")

// EnsurePolicyRecorded durably records policy as the run's current effective delegation-depth policy if it
// differs from the one already recorded (including "none recorded yet"). This is a plain AppendControl of
// kind "policy_set", not an Operation: there is no caller-facing idempotency key for it (it is a
// startup/recovery action performed once per process start by a single caller, not a user-facing repeatable
// request — T2.7), so the simpler read-then-maybe-write here is adequate; it is not meant to be called
// concurrently with itself.
//
// Returns changed=true when a new record was written (including the very first policy for a fresh run) —
// T2.7's "record any policy change as a durable operation before worker resumption" uses this to decide
// whether that record needed writing at all.
func (s *Store) EnsurePolicyRecorded(policy assistants.DepthPolicy, actor Actor) (changed bool, err error) {
	current := s.Snapshot().Assistants.Policy
	if current.Revision != "" && current.Revision == policy.Revision {
		return false, nil
	}

	payload, err := json.Marshal(policySetPayload{MaxDepth: policy.MaxDepth, Source: string(policy.Source), Revision: policy.Revision})
	if err != nil {
		return false, fmt.Errorf("marshal policy_set payload: %w", err)
	}
	c := &Control{Kind: kindPolicySet, SchemaVersion: ControlSchemaVersion, Actor: actor, Payload: payload}
	if err := s.AppendControl(c); err != nil {
		return false, err
	}
	return true, nil
}

// requiresPolicy reports whether any effect in the batch creates a task — task_created is the only effect
// kind whose Depth/MaxDepthAtCreate semantics depend on a recorded policy existing.
func requiresPolicy(effects []Effect) bool {
	for _, e := range effects {
		if e.Kind == kindTaskCreated {
			return true
		}
	}
	return false
}
