package afmsdk

import (
	"bytes"
	"net/http"
	"os/exec"
)

// Run is a handle to a single afm flow run, started as a subprocess by
// Client.Start. All fields are populated by Start; a zero-value Run is only
// useful in tests that exercise a single method directly.
type Run struct {
	cmd        *exec.Cmd
	dir        string
	baseURL    string
	httpClient *http.Client
	out        *bytes.Buffer
	exited     chan struct{}
	waitErr    error
}
