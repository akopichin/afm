// Package afmsdk lets a Go service launch, track, and control an afm flow run as a subprocess. Cleanup is
// always explicit — this package never deletes a run's isolated directory on its own.
package afmsdk
