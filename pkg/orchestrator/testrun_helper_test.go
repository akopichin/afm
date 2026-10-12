package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/orchestrator"
)

// runOrchestratorAsync registers cancel-and-wait cleanup after store cleanup, so LIFO stops agents before
// closing the Store.
func runOrchestratorAsync(ctx context.Context, t *testing.T, orch *orchestrator.Orchestrator, cancel context.CancelFunc) {
	t.Helper()
	done := make(chan struct{})
	go func() { _ = orch.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("orch.Run did not return within 10s of cancellation")
		}
	})
}
