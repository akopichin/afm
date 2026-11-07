package afmsdk

import (
	"errors"
	"fmt"
)

// Mode selects how Client.Start launches afm.
type Mode string

const (
	// ModeDashboard runs afm with the Web UI. The zero Mode behaves the same and passes no --mode flag,
	// so binaries that predate --mode keep working.
	ModeDashboard Mode = "dashboard"
	// ModeCore runs afm as an API-only core without the Web UI.
	ModeCore Mode = "core"
)

var (
	// ErrIncompatibleRuntime means the afm on the other end does not speak the runtime contract this SDK needs.
	ErrIncompatibleRuntime = errors.New("afmsdk: incompatible afm runtime")
	// ErrResultUnavailable means the workflow outcome was not observed before afm became unreachable.
	ErrResultUnavailable = errors.New("afmsdk: run result unavailable")
	// ErrNotOwned means the operation needs an afm process started by this Client.
	ErrNotOwned = errors.New("afmsdk: run is not owned by this client")
	// ErrKilled means afm ignored SIGINT within the grace period and was killed.
	ErrKilled = errors.New("afmsdk: afm did not stop in time and was killed")
)

// RunChangedError reports that the API now serves a different run than the one being awaited.
type RunChangedError struct {
	Expected string
	Current  string
}

func (e *RunChangedError) Error() string {
	return fmt.Sprintf("afmsdk: run changed from %q to %q", e.Expected, e.Current)
}

func (m Mode) validate() error {
	switch m {
	case "", ModeDashboard, ModeCore:
		return nil
	}
	return fmt.Errorf("afmsdk: unknown mode %q", m)
}
