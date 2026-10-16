// Package execprep builds the pre-resolved set of agent commands a run is allowed to execute (dev-plan.md
// Этап 8, T8.5, §7.3). Today wrappers/credentials/docker mounts are prepared once from the static flow at
// startup; this package extends that single preparation pass to also cover assistant (helper-agent) profile
// commands, and gives future dynamic-stage code (Этап 9) ONE set to consult — a command not in the Set a run
// started with can never become available just because config.yaml or flow.yaml changed on disk afterward.
package execprep

import (
	"cmp"
	"fmt"
	"slices"
)

// CommandRef names one command this run cares about, plus a reference to WHERE it is configured — never a
// secret value. Used both for required (flow stage) commands and optional (assistant profile) commands.
type CommandRef struct {
	Command string
	// ConfigRef is a human-readable pointer into config/flow (e.g. "flow.stages[build].command",
	// "collaboration.profiles.frontendDeveloper") — safe to log or persist durably (TestJournal_StoresConfigRefsNotSecretValues).
	ConfigRef string
}

// Resource is one command's preparation outcome.
type Resource struct {
	Command   string
	Required  bool
	Available bool
	// Reason is non-empty exactly when Available is false.
	Reason string
	// ConfigRef mirrors CommandRef.ConfigRef — a reference, never a credential value.
	ConfigRef string
}

// Set is the immutable, pre-resolved command manifest for one run, built ONCE by Prepare. A command not in
// Set (or present but Available:false) can never be picked by a dynamically added stage — picking an
// executor always consults an already-built Set, never rescans live config (TestDynamic_CanOnlyPickPreparedExecutor).
type Set struct {
	resources map[string]Resource
}

// Prepare resolves every required and optional command exactly once via resolve.
//
// required commands preserve today's fail-fast preflight: if resolve rejects one, Prepare returns an error
// and no Set at all — a run must not start with a missing/invalid required executor.
//
// optional commands (assistant profiles) never fail Prepare: an unresolved optional command is recorded with
// Available:false and a Reason, mirroring assistants.BuildRegistry's "missing provider marks the profile,
// doesn't break the run" posture (pkg/assistants/registry.go).
//
// A command listed in both required and optional is recorded once, as required.
func Prepare(required, optional []CommandRef, resolve func(cmd string) (ok bool, reason string)) (*Set, error) {
	resources := make(map[string]Resource, len(required)+len(optional))
	for _, c := range required {
		ok, reason := resolve(c.Command)
		if !ok {
			return nil, fmt.Errorf("execprep: required command %q is not available: %s", c.Command, reason)
		}
		resources[c.Command] = Resource{Command: c.Command, Required: true, Available: true, ConfigRef: c.ConfigRef}
	}
	for _, c := range optional {
		if _, exists := resources[c.Command]; exists {
			continue // already prepared as a required command
		}
		ok, reason := resolve(c.Command)
		resources[c.Command] = Resource{Command: c.Command, Required: false, Available: ok, Reason: reason, ConfigRef: c.ConfigRef}
	}
	return &Set{resources: resources}, nil
}

// IsPrepared reports whether cmd was resolved by Prepare AND is available. nil-safe: a nil *Set (preparation
// never ran, or ran before this command existed) reports false for everything — exactly the fail-closed
// behaviour a dynamically added stage must get for an unprepared command.
func (s *Set) IsPrepared(cmd string) bool {
	if s == nil {
		return false
	}
	r, ok := s.resources[cmd]
	return ok && r.Available
}

// Resource returns cmd's full preparation outcome, if Prepare considered it at all.
func (s *Set) Resource(cmd string) (Resource, bool) {
	if s == nil {
		return Resource{}, false
	}
	r, ok := s.resources[cmd]
	return r, ok
}

// All returns every prepared resource, sorted by command, for enumeration/journaling
// (TestJournal_StoresConfigRefsNotSecretValues reads this to prove no secret value ever entered a Resource).
func (s *Set) All() []Resource {
	if s == nil {
		return nil
	}
	out := make([]Resource, 0, len(s.resources))
	for _, r := range s.resources {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Resource) int { return cmp.Compare(a.Command, b.Command) })
	return out
}
