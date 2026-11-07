package agentcomm

import (
	"errors"
	"fmt"
	"strings"
)

// ErrAliasNotFound is returned by ResolveAlias when no roster participant's Alias matches.
var ErrAliasNotFound = errors.New("agentcomm: unknown alias")

// AmbiguousAliasError is returned by ResolveAlias when more than one roster participant shares
// the same Alias (case-insensitively). Candidates lists every match so the caller can ask the
// sender to disambiguate, rather than silently picking one.
type AmbiguousAliasError struct {
	Alias      string
	Candidates []ParticipantID
}

func (e *AmbiguousAliasError) Error() string {
	return fmt.Sprintf("agentcomm: alias %q matches %d participants", e.Alias, len(e.Candidates))
}

// ResolveAlias resolves a short, human-typed mention — e.g. "qa", optionally prefixed with "@" —
// to its canonical ParticipantID, matching case-insensitively against each roster Participant's
// Alias. It is meant to be the single alias-resolution implementation in the tree:
// pkg/sideagent.Manager's Config.MentionResolve currently does its own ad-hoc matching
// (pkg/sideagent/manager.go:78-84) and is re-pointed at this function in a later phase, not
// duplicated.
//
// The value returned is always the stored, canonical ParticipantID — never the original alias
// text — so history and addressing never depend on an alias that could later be renamed or
// reused. nil-safe: a nil or empty roster is just "no match", never a panic. A participant with
// an empty Alias is never a match candidate (so an empty lookup string cannot accidentally match
// it).
func ResolveAlias(alias string, roster []Participant) (ParticipantID, error) {
	needle := strings.ToLower(strings.TrimPrefix(alias, "@"))
	var candidates []ParticipantID
	for _, p := range roster {
		if p.Alias == "" {
			continue
		}
		if strings.ToLower(p.Alias) == needle {
			candidates = append(candidates, p.ID)
		}
	}
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("%w: %q", ErrAliasNotFound, alias)
	case 1:
		return candidates[0], nil
	default:
		return "", &AmbiguousAliasError{Alias: alias, Candidates: candidates}
	}
}

// CanAddress reports whether from may address to at all, encoding agent-mesh.md §5.2's
// addressing matrix: main<->helper, helper<->helper (even across different delegation trees),
// main<->main, a run-scoped role <-> anyone, a side agent <-> anyone in its own run, and the
// human <-> anyone. Every Participant this function is given is assumed to already belong to the
// same run — cross-run isolation is a higher layer's job; this package has no "run" type to
// express it with.
//
// Every recognized (Kind, Kind) pair is addressable: delegation depth limits restrict who may
// CREATE new children, never the right to message an existing colleague ("Глубина дерева
// ограничивает делегирование, а не возможность написать коллеге" — agent-mesh.md §5.2).
// CanAddress only rejects a participant whose Kind this build does not recognize (including the
// zero Participant) — reachability (Participant.Reachable) and thread membership are separate
// checks made elsewhere (ValidateMessage, the orchestrator's roster lookup).
func CanAddress(from, to Participant) bool {
	return from.Kind.Valid() && to.Kind.Valid()
}
