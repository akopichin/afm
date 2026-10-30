package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnerEpoch_SharedFileMutation(t *testing.T) {
	s, dir := openRunningStore(t, "a", "b")
	epoch := s.Stage("a").OwnerEpoch
	path := filepath.Join(dir, "feedback.md")
	// A held file callback leaves reads and other stages available. Synchronize
	// its lifetime explicitly so no Store is closed while an operation is active.
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- s.WithStageOwner("a", epoch, func() error {
			close(entered)
			<-release
			return os.WriteFile(path, []byte("current feedback"), 0600)
		})
	}()
	<-entered
	st := s.Stage("a")
	_ = s.Snapshot()
	otherErr := s.Apply(&Transition{StageID: "b", From: StatusRunning, To: StatusDone, Event: "complete"})
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if otherErr != nil {
		t.Fatal(otherErr)
	}
	if st.OwnerEpoch != epoch {
		t.Fatal("owner changed inside file callback")
	}
	mustApply(t, s, Transition{StageID: "a", From: StatusRunning, To: StatusPaused, Event: "pause"})
	next := mustApply(t, s, Transition{StageID: "a", From: StatusPaused, To: StatusRunning, Event: "continue"})
	if err := s.WithStageOwner("a", next.Seq, func() error { return os.WriteFile(path, []byte("successor feedback"), 0600) }); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"overwrite", "clear"} {
		t.Run(op, func(t *testing.T) {
			err := s.WithStageOwner("a", epoch, func() error {
				if op == "clear" {
					return os.Remove(path)
				}
				return os.WriteFile(path, []byte("stale feedback"), 0600)
			})
			if !errors.Is(err, ErrConcurrentChange) {
				t.Fatalf("stale callback error=%v", err)
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != "successor feedback" {
				t.Fatalf("successor file=%q err=%v", data, err)
			}
		})
	}
	marker := errors.New("file write failed")
	if err := s.WithStageOwner("a", next.Seq, func() error { return marker }); !errors.Is(err, marker) {
		t.Fatalf("file error lost: %v", err)
	}
}
