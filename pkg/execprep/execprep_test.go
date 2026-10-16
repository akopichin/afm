package execprep_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/execprep"
)

func alwaysOK(string) (bool, string) { return true, "" }

// TestPrepare_IncludesConfiguredProfiles: both flow-stage (required) and assistant-profile (optional)
// commands end up prepared and available after one Prepare call.
func TestPrepare_IncludesConfiguredProfiles(t *testing.T) {
	set, err := execprep.Prepare(
		[]execprep.CommandRef{{Command: "claude", ConfigRef: "flow.stages[build].command"}},
		[]execprep.CommandRef{{Command: "frontend-reviewer", ConfigRef: "collaboration.profiles.frontendDeveloper"}},
		alwaysOK,
	)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !set.IsPrepared("claude") {
		t.Error(`IsPrepared("claude") = false, want true (required flow command)`)
	}
	if !set.IsPrepared("frontend-reviewer") {
		t.Error(`IsPrepared("frontend-reviewer") = false, want true (optional assistant profile command)`)
	}
	if set.IsPrepared("never-mentioned") {
		t.Error(`IsPrepared("never-mentioned") = true, want false`)
	}
}

// TestPrepare_MissingOptionalProviderDoesNotBreakRun: an optional (assistant-only) command that fails
// resolution is marked unavailable, not fatal; a REQUIRED command failing resolution still fails Prepare
// (today's fail-fast flow-stage preflight is preserved).
func TestPrepare_MissingOptionalProviderDoesNotBreakRun(t *testing.T) {
	resolve := func(cmd string) (bool, string) {
		if cmd == "missing-helper" {
			return false, "not found on PATH"
		}
		return true, ""
	}

	set, err := execprep.Prepare(
		[]execprep.CommandRef{{Command: "claude", ConfigRef: "client.command"}},
		[]execprep.CommandRef{{Command: "missing-helper", ConfigRef: "collaboration.profiles.reviewer"}},
		resolve,
	)
	if err != nil {
		t.Fatalf("Prepare returned an error for a missing OPTIONAL provider: %v", err)
	}
	if set.IsPrepared("missing-helper") {
		t.Error(`IsPrepared("missing-helper") = true, want false`)
	}
	res, ok := set.Resource("missing-helper")
	if !ok {
		t.Fatal(`Resource("missing-helper") not found — Prepare must still record it, just unavailable`)
	}
	if res.Available || res.Reason == "" || res.Required {
		t.Fatalf("Resource(missing-helper) = %+v, want Available=false Required=false Reason=non-empty", res)
	}

	// A REQUIRED command failing resolution is still fatal — fail-fast preserved.
	_, err = execprep.Prepare(
		[]execprep.CommandRef{{Command: "missing-helper", ConfigRef: "flow.stages[build].command"}},
		nil,
		resolve,
	)
	if err == nil {
		t.Fatal("Prepare succeeded for a missing REQUIRED command, want an error")
	}
}

// TestDynamic_CanOnlyPickPreparedExecutor: a command added to config AFTER a Set was built must not
// silently become available through that SAME Set — only a fresh Prepare call (a new preparation pass)
// makes it available.
func TestDynamic_CanOnlyPickPreparedExecutor(t *testing.T) {
	setA, err := execprep.Prepare(
		[]execprep.CommandRef{{Command: "claude", ConfigRef: "client.command"}},
		nil,
		alwaysOK,
	)
	if err != nil {
		t.Fatal(err)
	}
	if setA.IsPrepared("new-helper") {
		t.Fatal(`IsPrepared("new-helper") = true on the ORIGINAL Set, want false`)
	}

	// A config edit adds "new-helper" — simulated by a SECOND, independent Prepare call (a fresh preparation
	// pass), not by mutating setA.
	setB, err := execprep.Prepare(
		[]execprep.CommandRef{{Command: "claude", ConfigRef: "client.command"}, {Command: "new-helper", ConfigRef: "collaboration.profiles.newHelper"}},
		nil,
		alwaysOK,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !setB.IsPrepared("new-helper") {
		t.Fatal(`IsPrepared("new-helper") = false on the fresh Set, want true`)
	}
	// setA (still the one in use until a fresh pass replaces it) remains unaffected by setB's existence.
	if setA.IsPrepared("new-helper") {
		t.Fatal(`setA started reporting "new-helper" as prepared after an unrelated Prepare call — Set must be immutable`)
	}
}

// TestJournal_StoresConfigRefsNotSecretValues: whatever gets durably recorded about a prepared command
// (Resource, enumerated via All — the shape a future durable journal entry would serialize) is a
// command/config reference, never the secret value a resolve function might have consulted internally.
func TestJournal_StoresConfigRefsNotSecretValues(t *testing.T) {
	const secretValue = "sk-ant-super-secret-token-should-never-appear" //nolint:gosec // test fixture value, not a real credential
	resolve := func(cmd string) (bool, string) {
		// resolve may internally know/check a credential, but must never hand it back through (ok, reason).
		_ = secretValue // stands in for e.g. checking a resolved token's validity
		return true, ""
	}

	set, err := execprep.Prepare(
		[]execprep.CommandRef{{Command: "glm51", ConfigRef: "docker.agents.glm51"}},
		[]execprep.CommandRef{{Command: "frontend-reviewer", ConfigRef: "collaboration.profiles.frontendDeveloper"}},
		resolve,
	)
	if err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(set.All())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secretValue) {
		t.Fatalf("durable-journal-shaped output contains the secret value:\n%s", data)
	}
	for _, r := range set.All() {
		if strings.Contains(r.ConfigRef, secretValue) || strings.Contains(r.Reason, secretValue) {
			t.Fatalf("Resource %+v leaks the secret value", r)
		}
	}
}

// Nil Set is fail-closed, not a panic — mirrors assistants.Registry's nil-receiver convention.
func TestSet_NilIsFailClosed(t *testing.T) {
	var set *execprep.Set
	if set.IsPrepared("anything") {
		t.Fatal("nil Set reports a command as prepared")
	}
	if _, ok := set.Resource("anything"); ok {
		t.Fatal("nil Set resolves a Resource")
	}
	if set.All() != nil {
		t.Fatal("nil Set.All() != nil")
	}
}
