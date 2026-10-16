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

// AvailabilityCheck reports whether a command can run a helper profile at all (ok), why not if it can't
// (reason — non-empty exactly when ok is false), and which read-only guarantee backs a positive answer
// (enforcement — meaningful only when ok is true). Production callers build this around
// config.HelperCommandRejection + config.HelperReadonlyEnforcement (cmd/afm composition); pkg/assistants
// itself stays neutral and does not import pkg/config.
type AvailabilityCheck func(command string) (ok bool, reason string, enforcement ReadonlyEnforcement)

// ProfileView is the read-only projection of a registry entry exposed to callers (HTTP, task creation): the
// profile's public fields plus its CURRENT availability. Recomputed every time BuildRegistry runs; List()
// returns the snapshot taken at that time.
type ProfileView struct {
	ID                  ProfileID
	Title               string
	Command             string
	Available           bool
	UnavailableReason   string
	ReadonlyEnforcement ReadonlyEnforcement
}

// Registry is the immutable, resolved set of assistant profiles for a run: three layers (builtin → global →
// project) folded by id, where a later layer's definition fully REPLACES an earlier one with the same id
// (never a field-by-field merge). Availability is a separate concern from "configured": an
// unknown/unavailable command marks a profile unavailable without failing the whole build.
type Registry struct {
	entries map[ProfileID]registryEntry
	// byLower indexes every profile id by its lowercased form, for case-insensitive `@mention` resolution
	// (T4.1). Built once in BuildRegistry, which also rejects a config where two DIFFERENT ids collide once
	// lowercased — that config would make `@mention` resolution ambiguous.
	byLower map[string]ProfileID
}

type registryEntry struct {
	profile Profile
	view    ProfileView
}

// BuildRegistry folds builtin, global and project profile layers (in that priority order — a later layer's
// profile with the same id fully replaces the earlier one, not a field merge) and computes each resulting
// profile's availability via `available`. defaultCommand is substituted for any BUILTIN profile left with
// an empty Command ("a builtin role with no command inherits the client's default command", §3.5);
// global/project profiles are user-authored and must name their own command — they never inherit it.
//
// Returns an error only for structural problems that would corrupt task storage or identity later: a
// duplicate id within a single layer, or an id unsafe to use as a directory name (a task's workspace is
// keyed by its profile id, T3.x). An unavailable (but structurally valid) command is NOT an error — see
// AvailabilityCheck.
func BuildRegistry(builtin, global, project []Profile, defaultCommand string, available AvailabilityCheck) (*Registry, error) {
	reg := &Registry{entries: map[ProfileID]registryEntry{}}

	type layerSpec struct {
		profiles       []Profile
		inheritDefault bool
	}
	layers := []layerSpec{
		{builtin, true},
		{global, false},
		{project, false},
	}

	for _, l := range layers {
		seen := make(map[ProfileID]bool, len(l.profiles))
		for _, p := range l.profiles {
			if seen[p.ID] {
				return nil, fmt.Errorf("assistants: duplicate profile id %q within one layer", p.ID)
			}
			seen[p.ID] = true
			if !profileIDSafe(string(p.ID)) {
				return nil, fmt.Errorf("assistants: profile id %q is not safe for use as a path component", p.ID)
			}
			if l.inheritDefault && p.Command == "" {
				p.Command = defaultCommand
			}
			ok, reason, enforcement := available(p.Command)
			reg.entries[p.ID] = registryEntry{
				profile: p,
				view: ProfileView{
					ID:                  p.ID,
					Title:               p.Title,
					Command:             p.Command,
					Available:           ok,
					UnavailableReason:   reason,
					ReadonlyEnforcement: enforcement,
				},
			}
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

// List returns every registered profile's view, sorted by id for a stable, deterministic order (map
// iteration order is not stable across Go runs).
func (r *Registry) List() []ProfileView {
	ids := make([]string, 0, len(r.entries))
	for id := range r.entries {
		ids = append(ids, string(id))
	}
	slices.Sort(ids)
	out := make([]ProfileView, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.entries[ProfileID(id)].view)
	}
	return out
}

// Get returns the full profile definition by id — used when creating a task: ProfileSnapshot is a copy of
// this value, frozen at creation time.
func (r *Registry) Get(id ProfileID) (Profile, bool) {
	e, ok := r.entries[id]
	if !ok {
		return Profile{}, false
	}
	return e.profile, true
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
