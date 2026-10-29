//go:build unix

package orchestrator_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

// Отмена ждёт агентов: к возврату Run процесс соседней стадии уже завершён.
func TestExitOnFailShutdownWait(t *testing.T) {
	stages := []flow.Stage{
		// bad ждёт pid соседа, иначе провал мог бы случиться раньше его старта.
		{ID: "bad", Name: "Bad", Script: "until [ -s slow.pid ]; do sleep 0.01; done; exit 1"},
		{ID: "slow", Name: "Slow", Script: "echo $$ > slow.pid.tmp; mv slow.pid.tmp slow.pid; exec sleep 60"},
	}
	r := newExitOnFailRun(t, stages, true)

	_ = requireStageFailed(t, r.run(t), "bad")

	raw, err := os.ReadFile(filepath.Join(r.rootDir, "slow.pid"))
	if err != nil {
		t.Fatalf("neighbour never started: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("pid file %q: %v", raw, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("neighbour process %d still alive after Run returned (kill -0: %v)", pid, err)
	}
}
