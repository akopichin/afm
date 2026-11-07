// Package afmsdk lets a Go service launch, track, and control an afm flow run as a subprocess. Cleanup is
// always explicit — this package never deletes a run's isolated directory on its own.
//
// Config.Mode selects the dashboard (default) or the API-only core (ModeCore). Run.WaitResult reports the
// workflow outcome, Run.Shutdown stops the process this Client started, and Run.Wait/Run.Cleanup keep their
// process-level meaning. Handles from Connect and Attach do not own a process.
package afmsdk
