package config

import "testing"

// SetContainerMarkerPresentForTest substitutes the container-marker detector
// used by InContainer, restoring the real one when the test ends. Tests use it
// so they never consult the real /.dockerenv — a CI run that itself happens to
// execute inside a container would otherwise flip the result.
func SetContainerMarkerPresentForTest(t *testing.T, present bool) {
	t.Helper()
	prev := containerMarkerPresent
	containerMarkerPresent = func() bool { return present }
	t.Cleanup(func() { containerMarkerPresent = prev })
}

// SetContainerMarkerPathsForTest points the real detector at the given paths,
// restoring the defaults when the test ends. Lets a test exercise
// DefaultContainerMarkerPresent's actual os.Stat logic against a temp file
// instead of the real /.dockerenv.
func SetContainerMarkerPathsForTest(t *testing.T, paths []string) {
	t.Helper()
	prev := containerMarkerPaths
	containerMarkerPaths = paths
	t.Cleanup(func() { containerMarkerPaths = prev })
}

// ProbeContainerMarkersForTest exposes the unexported real detector so a test
// can verify its filesystem behavior directly.
func ProbeContainerMarkersForTest() bool { return defaultContainerMarkerPresent() }
