package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

// pipelineHarness wires an orchestrator with memory enabled and a scripted
// runMemoryAgent stub that simulates each agent's file effects.
func pipelineHarness(t *testing.T, maxBytes, retries int) (*Orchestrator, string, string, *[]string) {
	t.Helper()
	o := newTestOrchestrator(t)
	runDir := t.TempDir()
	proj := filepath.Join(t.TempDir(), "PROJECT_MEMORY.md")
	sess := filepath.Join(runDir, "SESSION_MEMORY.md")
	o.opts.RunDir = runDir
	o.opts.Memory = flow.MemoryConfig{ProjectFile: proj, MaxBytes: maxBytes, CompressRetries: retries}
	o.opts.MemoryProjectPath = proj
	o.opts.MemorySessionPath = sess

	var order []string
	o.runMemoryAgent = func(ctx context.Context, spec memoryAgentSpec) error {
		order = append(order, spec.kind)
		switch spec.kind {
		case "reflect":
			_ = os.WriteFile(spec.datasetOut, []byte("project_level: []\nsession_level: []\n"), 0644)
		case "updater":
			// Simulate an oversized PROJECT file on first write.
			_ = os.WriteFile(spec.projectPath, []byte("# PROJECT MEMORY\n\n## A\n"+strings.Repeat("x", maxBytes+500)+"\n"), 0644)
			_ = os.WriteFile(spec.sessionPath, []byte("# SESSION MEMORY\n"), 0644)
		case "compressor":
			// Simulate a compressor that shrinks under the limit.
			_ = os.WriteFile(spec.targetFile, []byte("# PROJECT MEMORY\n\n## A\nsmall\n"), 0644)
		default:
			// no-op for any other kind
		}
		return nil
	}
	return o, proj, sess, &order
}

func TestReflectionPipeline_ReflectThenUpdaterThenCompress(t *testing.T) {
	o, proj, _, order := pipelineHarness(t, 1000, 2)
	o.runReflectionPipeline(context.Background(), "s1", []string{filepath.Join(o.opts.RunDir, "s1", "autonomous.log")}, filepath.Join(o.opts.RunDir, "s1"))
	got := strings.Join(*order, ",")
	if !strings.HasPrefix(got, "reflect,updater,compressor") {
		t.Errorf("order = %q, want reflect,updater,compressor…", got)
	}
	if fileExceeds(proj, 1000) {
		t.Error("project memory should be within limit after compression")
	}
}

func TestMaybeRunReflection_NoOpWhenDisabled(t *testing.T) {
	o := newTestOrchestrator(t)
	o.opts.MemoryProjectPath = "" // disabled
	called := false
	o.runMemoryAgent = func(ctx context.Context, spec memoryAgentSpec) error { called = true; return nil }
	o.maybeRunReflection(context.Background(), "s1")
	o.concurrency.WaitAgents()
	if called {
		t.Error("must not run any memory agent when memory disabled")
	}
}

func TestReflectionPipeline_Serialized(t *testing.T) {
	o, _, _, _ := pipelineHarness(t, 100000, 2)
	var mu sync.Mutex
	concurrent, maxConcurrent := 0, 0
	o.runMemoryAgent = func(ctx context.Context, spec memoryAgentSpec) error {
		mu.Lock()
		concurrent++
		if concurrent > maxConcurrent {
			maxConcurrent = concurrent
		}
		mu.Unlock()
		// small yield to expose overlap if not serialized
		mu.Lock()
		concurrent--
		mu.Unlock()
		return nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Go(func() { o.runReflectionPipeline(context.Background(), "s", nil, t.TempDir()) })
	}
	wg.Wait()
	if maxConcurrent > 1 {
		t.Errorf("pipelines overlapped: maxConcurrent=%d (reflectMu not held)", maxConcurrent)
	}
}
