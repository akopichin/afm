//go:build !linux && !darwin

package runregistry

// ProcessStart is not available on this platform.
func ProcessStart(int) (string, error) {
	return "", ErrUnsupported
}
