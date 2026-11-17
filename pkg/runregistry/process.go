package runregistry

import "errors"

// ErrUnsupported means the platform cannot read a process start marker.
var ErrUnsupported = errors.New("runregistry: process start marker is not supported on this platform")
