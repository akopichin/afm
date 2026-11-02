package assistants

import (
	"fmt"
	"slices"
	"strings"
)

// ReadonlyEnforcement records which read-only guarantee was backing a profile's availability at the moment
// it was computed: "os" when a verifiable OS sandbox enforces it (Seatbelt/Landlock via the codex read-only
// adapter), "none" when the only boundary is the container itself, or when the profile is not available at
// all. Frozen into a task's ProfileSnapshot at creation time (T3.4) so history does not get silently
// reinterpreted if config changes later — see dev-plan.md Этап 1 T1.5.
type ReadonlyEnforcement string

const (
	ReadonlyEnforcementOS   ReadonlyEnforcement = "os"
	ReadonlyEnforcementNone ReadonlyEnforcement = "none"
)

// WeakerThan reports whether e is a STRICTLY weaker read-only guarantee than other — "os" (a verifiable OS
// sandbox) is strictly stronger than "none" (no OS guarantee, boundary is the container/prompt only); any
// other value (including both equal) is never weaker. Этап 10 (T10.2, "no self elevation"): a delegating
// assistant must never be able to hand a child a less-restricted executor than its own — this is the one
// comparison that check needs, kept here (next to the two constants it orders) rather than re-derived at
// each call site.
func (e ReadonlyEnforcement) WeakerThan(other ReadonlyEnforcement) bool {
	return e == ReadonlyEnforcementNone && other == ReadonlyEnforcementOS
}

// ProfileView is a profile as a caller sees it where a task would run: the executor it resolves to, where that
// came from, and whether this process may run it. Built per stage by the orchestrator, never stored.
type ProfileView struct {
	ID                  ProfileID
	Title               string
	Command             string
	CommandSource       CommandSource
	RuntimeContext      RuntimeContext
	Available           bool
	UnavailableReason   string
	ReadonlyEnforcement ReadonlyEnforcement
}

// DefaultProfileID is the builtin profile every run has: a general role with no command of its own, so it runs
// the executor of wherever it is attached.
const DefaultProfileID ProfileID = "default"

// BuiltinProfiles returns the builtin layer for BuildRegistry. A global or project profile with the same id
// replaces it as a whole.
func BuiltinProfiles() []Profile {
	return []Profile{{
		ID:           DefaultProfileID,
		Title:        "Default",
		Instructions: "Help with the request you are given: study the project as far as it needs and answer with concrete, checked findings.",
	}}
}

// Registry is the immutable set of a run's assistant profiles, layers folded by id (see BuildRegistry). It holds
// roles only: the executor and whether it may run are decided per task, where it is attached (ResolveExecution).
type Registry struct {
	entries map[ProfileID]Profile
	// byLower indexes every profile id by its lowercased form, for case-insensitive `@mention` resolution. Built
	// once in BuildRegistry, which also rejects a config where two DIFFERENT ids collide once lowercased — that
	// config would make `@mention` resolution ambiguous.
	byLower map[string]ProfileID
}

// BuildRegistry folds builtin, global and project profile layers in that priority order — a later layer's profile
// with the same id fully replaces the earlier one, not a field merge.
//
// Returns an error only for structural problems that would corrupt task storage or identity later: a duplicate id
// within a single layer, or an id unsafe to use as a directory name (a task's workspace is keyed by its profile id).
func BuildRegistry(builtin, global, project []Profile) (*Registry, error) {
	reg := &Registry{entries: map[ProfileID]Profile{}}
	for _, layer := range [][]Profile{global, project} {
		if err := foldLayer(reg.entries, layer); err != nil {
			return nil, err
		}
	}
	builtins := map[ProfileID]Profile{}
	if err := foldLayer(builtins, builtin); err != nil {
		return nil, err
	}
	// A configured id that differs from a builtin one only in case replaces it like the same id would: the user
	// never wrote the builtin, so it must not make their config ambiguous. Two configured ids still collide below.
	for id, b := range builtins {
		if !containsIDIgnoringCase(reg.entries, id) {
			reg.entries[id] = b
		}
	}

	byLower := make(map[string]ProfileID, len(reg.entries))
	for id := range reg.entries {
		lower := strings.ToLower(string(id))
		if existing, collide := byLower[lower]; collide && existing != id {
			return nil, fmt.Errorf("assistants: profile ids %q and %q collide once case-normalized — @mention resolution would be ambiguous", existing, id)
		}
		byLower[lower] = id
	}
	reg.byLower = byLower
	return reg, nil
}

// foldLayer adds one layer's profiles to entries, replacing a same-id profile from an earlier layer.
func foldLayer(entries map[ProfileID]Profile, layer []Profile) error {
	seen := make(map[ProfileID]bool, len(layer))
	for _, p := range layer {
		if seen[p.ID] {
			return fmt.Errorf("assistants: duplicate profile id %q within one layer", p.ID)
		}
		seen[p.ID] = true
		if !profileIDSafe(string(p.ID)) {
			return fmt.Errorf("assistants: profile id %q is not safe for use as a path component", p.ID)
		}
		entries[p.ID] = p
	}
	return nil
}

func containsIDIgnoringCase(entries map[ProfileID]Profile, id ProfileID) bool {
	for existing := range entries {
		if strings.EqualFold(string(existing), string(id)) {
			return true
		}
	}
	return false
}

// Resolve looks up a profile by a case-insensitive id, used to turn `@mention` text into a profile: a
// mention addresses a ROLE, never a running task instance (addressing an already-running task is by
// task_id — see dev-plan.md T4.7, Этап 4). An unrecognized mention is not an error: ok=false lets the
// caller leave the text untouched (dev-plan.md §4.1). nil-safe — a nil *Registry (collaboration capability
// off, same "nil-receiver → off" convention as cmd/afm/run_side_agent.go's service()) just never resolves
// anything, so callers (e.g. sideagent.Config.MentionResolve) don't need their own nil check.
func (r *Registry) Resolve(mention string) (ProfileID, bool) {
	if r == nil {
		return "", false
	}
	id, ok := r.byLower[strings.ToLower(mention)]
	return id, ok
}

// List returns every registered profile, sorted by id for a stable, deterministic order (map iteration order is
// not stable across Go runs).
func (r *Registry) List() []Profile {
	ids := make([]string, 0, len(r.entries))
	for id := range r.entries {
		ids = append(ids, string(id))
	}
	slices.Sort(ids)
	out := make([]Profile, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.entries[ProfileID(id)])
	}
	return out
}

// Get returns the full profile definition by id — used when creating a task: ProfileSnapshot is a copy of
// this value, frozen at creation time.
func (r *Registry) Get(id ProfileID) (Profile, bool) {
	p, ok := r.entries[id]
	return p, ok
}

// profileIDSafe mirrors pkg/flow's stage-id safety check (StageIDSafe): a profile id becomes a task
// workspace directory name later (T3.x), so the same restrictive charset applies — reject empty, ".", "..",
// path separators, and any other character outside [A-Za-z0-9._-].
func profileIDSafe(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}
