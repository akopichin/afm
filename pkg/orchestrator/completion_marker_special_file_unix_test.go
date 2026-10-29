//go:build unix

package orchestrator

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
)

// readMarkerWithin reads the marker of s1 and fails the test if that takes longer than a moment: the read runs
// before every turn, so an agent-made special file must not be able to block it.
func readMarkerWithin(t *testing.T, o *Orchestrator, stageDir string) completionMarker {
	t.Helper()
	done := make(chan completionMarker, 1)
	go func() { done <- o.readCompletionMarker(flow.Stage{ID: "s1"}, stageDir, phaseImplementation) }()
	select {
	case m := <-done:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("reading the marker blocked")
		return completionMarker{}
	}
}

// A marker that is a FIFO or a link to an endless device is not a marker: it is neither read nor valid.
func TestCompletionMarker_SpecialFilesAreNotRead(t *testing.T) {
	cases := map[string]func(t *testing.T, path string){
		"fifo": func(t *testing.T, path string) {
			if err := syscall.Mkfifo(path, 0o644); err != nil {
				t.Fatal(err)
			}
			// Unblocks a reader stuck in open, so a failing run does not leak its goroutine.
			t.Cleanup(func() {
				if f, err := os.OpenFile(path, os.O_RDWR, 0); err == nil {
					_ = f.Close()
				}
			})
		},
		"symlink to an endless device": func(t *testing.T, path string) {
			if err := os.Symlink("/dev/zero", path); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, create := range cases {
		t.Run(name, func(t *testing.T) {
			o, runDir := setupHookOrch(t, "s1")
			stageDir := filepath.Join(runDir, "s1")
			if err := os.MkdirAll(stageDir, 0o755); err != nil {
				t.Fatal(err)
			}
			create(t, filepath.Join(stageDir, ".done"))

			m := readMarkerWithin(t, o, stageDir)

			if m.present || m.valid {
				t.Fatalf("marker = %+v, want absent and invalid", m)
			}
		})
	}
}
