package assistants_test

import (
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

// TestDelegate_RequiresDiscriminator — a "delegate"-shaped body (children array) with no "kind" at all must
// be rejected outright, never guessed from field presence (the same rule
// TestAssistantResult_RequiresKindDiscriminator already proves for "result").
func TestDelegate_RequiresDiscriminator(t *testing.T) {
	raw := `{"schema_version":1,"children":[{"profile_id":"p","title":"t","instruction":"i","mode":"advisory","trigger":"one_shot"}]}`
	_, err := assistants.DecodeResult([]byte(raw))
	if err == nil {
		t.Fatal("DecodeResult with no kind: want rejection, got nil error")
	}
}

// TestDelegate_NotTreatedAsCompletedCheck proves a decoded "delegate" envelope can never be mistaken for a
// turn that closes a required-mode gate — only Envelope.ClosesRequiredGate() on a "result" envelope with
// verdict:"pass" can. A delegate has no verdict at all; the orchestrator must never fall back to treating
// its mere presence (or absence of an explicit rejection) as an implicit pass.
func TestDelegate_NotTreatedAsCompletedCheck(t *testing.T) {
	raw := `{"schema_version":1,"kind":"delegate","children":[` +
		`{"profile_id":"p","title":"t","instruction":"i","mode":"advisory","trigger":"one_shot"}]}`
	env, err := assistants.DecodeResult([]byte(raw))
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	if env.Kind != assistants.KindDelegate || env.Delegate == nil {
		t.Fatalf("env = %+v, want a decoded delegate", env)
	}
	if env.ClosesRequiredGate() {
		t.Fatal("a kind:delegate envelope must never close a required-mode gate")
	}
}

// TestDelegate_ChildCountCapped — a delegate group requesting more than assistants.MaxDelegateChildren
// children in one turn is rejected outright (no silent truncation to the first N).
func TestDelegate_ChildCountCapped(t *testing.T) {
	var children []string
	for i := 0; i <= assistants.MaxDelegateChildren; i++ {
		children = append(children, `{"profile_id":"p","title":"t","instruction":"i","mode":"advisory","trigger":"one_shot"}`)
	}
	raw := `{"schema_version":1,"kind":"delegate","children":[` + strings.Join(children, ",") + `]}`
	_, err := assistants.DecodeResult([]byte(raw))
	if err == nil {
		t.Fatalf("DecodeResult with %d children (cap is %d): want rejection, got nil error", len(children), assistants.MaxDelegateChildren)
	}
}

// TestDelegate_RejectsSelfElevation — a child request cannot smuggle in capabilities wider than its parent's,
// a bigger max_depth, a different root_stage_id, or a separate budget: DelegateRequest's strict decoder
// (additionalProperties:false, mirrored by delegateChild in the schema) has no fields for any of these, so a
// model attempting to set them is rejected as an unknown field — the same mechanism that already rejects any
// other unexpected field, not a bespoke allow/deny list of elevation attempts.
func TestDelegate_RejectsSelfElevation(t *testing.T) {
	attempts := []string{
		`"capabilities":{"delegate":true,"browser":true}`,
		`"max_depth":99`,
		`"root_stage_id":"some-other-stage"`,
		`"budget":1000000`,
	}
	for _, field := range attempts {
		raw := `{"schema_version":1,"kind":"delegate","children":[` +
			`{"profile_id":"p","title":"t","instruction":"i","mode":"advisory","trigger":"one_shot",` + field + `}]}`
		if _, err := assistants.DecodeResult([]byte(raw)); err == nil {
			t.Fatalf("DecodeResult with a child smuggling %s: want rejection, got nil error", field)
		}
	}
}

// TestResult_ConsistencyWithRequiredChildren is recorded here (T4a.1) per dev-plan.md, but pkg/assistants
// cannot see a task's children — ValidateTree/BlocksRootCompletion-OF-children requires the task projection
// (pkg/state), which this package deliberately does not import (see types.go's package doc). The actual gate
// — a parent may not have its "pass" verdict accepted while a required child is not yet terminal+pass — is
// enforced at the orchestrator level once children exist at all (T4a.3's waiting_children/continuation
// wiring); see TestContinuation_ParentPassRejectedWithOpenRequiredChild in
// pkg/orchestrator/assistants_continuation_test.go for the enforced version of this rule. This note is the
// judgment call recorded per dev-plan.md's task-completion rule 9.
func TestResult_ConsistencyWithRequiredChildren(t *testing.T) {
	t.Skip("enforced at the orchestrator level — see pkg/orchestrator/assistants_continuation_test.go's " +
		"TestContinuation_ParentPassRejectedWithOpenRequiredChild (T4a.3), not decodable from the envelope alone")
}
