package executor

import (
	"os"
	"testing"
)

// TestMain lets the test binary serve as the process group anchor, as the afm binary does in main.
func TestMain(m *testing.M) {
	if ServeProcessGroupAnchor(os.Args) {
		return
	}
	m.Run()
}
