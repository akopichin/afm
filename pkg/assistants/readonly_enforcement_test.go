package assistants_test

import (
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

// TestReadonlyEnforcement_WeakerThan — Этап 10 (T10.2, "no self elevation"): "os" (a verifiable OS sandbox)
// is the only strictly STRONGER guarantee than "none" (no OS guarantee). Every other comparison, including
// equal values, must report false — admitChildren's self-elevation check (assistants_delegate.go) rejects a
// delegation ONLY on a genuine downgrade, never on a lateral or upgrading profile choice.
func TestReadonlyEnforcement_WeakerThan(t *testing.T) {
	cases := []struct {
		name        string
		child, want assistants.ReadonlyEnforcement
		weaker      bool
	}{
		{"none weaker than os", assistants.ReadonlyEnforcementNone, assistants.ReadonlyEnforcementOS, true},
		{"os not weaker than none", assistants.ReadonlyEnforcementOS, assistants.ReadonlyEnforcementNone, false},
		{"os not weaker than os", assistants.ReadonlyEnforcementOS, assistants.ReadonlyEnforcementOS, false},
		{"none not weaker than none", assistants.ReadonlyEnforcementNone, assistants.ReadonlyEnforcementNone, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.child.WeakerThan(c.want); got != c.weaker {
				t.Fatalf("%q.WeakerThan(%q) = %v, want %v", c.child, c.want, got, c.weaker)
			}
		})
	}
}
