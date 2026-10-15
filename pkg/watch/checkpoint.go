package watch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/memory"
)

// MaxCheckpointBytes bounds one checkpoint's durable context package — a long stage run must never grow a
// single checkpoint file without limit (dev-plan.md T5.2's TestCheckpoint_StorageBounded). Mirrors the same
// "cap on the full input, documented" convention as assistants.MaxResultBytes/verify.MaxResultBytes.
const MaxCheckpointBytes = 64 * 1024

// cursorFileName/checkpointFilePattern are the two files a CheckpointStore manages under its directory.
const cursorFileName = "cursor.json"

// cursorPayload is cursor.json's on-disk shape.
type cursorPayload struct {
	UpToSeq int `json:"up_to_seq"`
}

// Checkpoint is one bundle of normalized main-agent output a watcher is about to be shown, durably persisted
// BEFORE the cursor advances past it (see Commit's doc comment).
type Checkpoint struct {
	UpToSeq int
	Content string
}

// CheckpointStore is the durable, per-watcher-task checkpoint/cursor store (T5.2). It lives under one task's
// own directory (mirrors pkg/orchestrator/assistants_files.go's taskDir/result.json convention — a watcher's
// cursor is file-based state in exactly the same sense a regular task's result.json already is, not a new
// journal/Effect kind): nothing here is the run's SOLE source of truth for anything FSM-relevant — every
// actual task status transition still goes through the existing state.Operation/Effect journal unchanged. The
// cursor only ever answers "have I already shown the watcher observations up to seq N" — an idempotency/cache
// concern, the same kind pkg/state's own derived state.json snapshot already is for the whole run.
type CheckpointStore struct {
	dir string
}

// NewCheckpointStore builds a store rooted at dir (typically "<taskDir>/watch").
func NewCheckpointStore(dir string) *CheckpointStore {
	return &CheckpointStore{dir: dir}
}

// Cursor reads the durably-recorded "processed up to" position. ok is false if no checkpoint was ever
// committed yet (a brand new watcher) OR if the cursor file exists but cannot be read/parsed — in BOTH cases
// the caller must behave as "nothing processed yet" (TestCheckpoint_MissingLogMeansNotVerified: a missing or
// unreadable record can never be silently treated as "verified").
func (s *CheckpointStore) Cursor() (upToSeq int, ok bool) {
	raw, err := os.ReadFile(filepath.Join(s.dir, cursorFileName))
	if err != nil {
		return 0, false
	}
	var p cursorPayload
	if json.Unmarshal(raw, &p) != nil {
		return 0, false
	}
	return p.UpToSeq, true
}

// checkpointPath returns the path for the durable context package of the checkpoint ending at upToSeq.
func (s *CheckpointStore) checkpointPath(upToSeq int) string {
	return filepath.Join(s.dir, fmt.Sprintf("checkpoint-%08d.md", upToSeq))
}

// ErrCursorNotMonotonic is returned by Commit when upToSeq does not strictly advance the durable cursor —
// TestCheckpoint_CursorAdvancesMonotonically / TestDedup_AfterReconnectNoDuplicateResult: replaying the SAME
// (or an older) observation range a second time (e.g. after a reconnect/restart that re-fed already-processed
// observations) must never re-bundle or re-count it as a new checkpoint.
var ErrCursorNotMonotonic = errors.New("watch: checkpoint upToSeq does not advance the cursor")

// Commit durably records one checkpoint: the context package is written to disk FIRST (atomic temp+rename,
// pkg/memory.AtomicWrite — the SAME primitive every other durable artifact in this codebase uses), and only
// once that succeeds does the cursor file advance to reference it SECOND
// (TestCheckpoint_ContextDurableBeforeReferenceCommitted) — if the process crashes between the two writes, the
// cursor still reflects the LAST successfully-referenced checkpoint, and the orphaned checkpoint file (written
// but never referenced) is simply inert, never mistaken for "processed".
func (s *CheckpointStore) Commit(cp Checkpoint) error {
	if cur, ok := s.Cursor(); ok && cp.UpToSeq <= cur {
		return ErrCursorNotMonotonic
	}
	content := cp.Content
	if len(content) > MaxCheckpointBytes {
		content = content[:MaxCheckpointBytes] + "\n\n[truncated: checkpoint content exceeded the size cap]\n"
	}
	if err := memory.AtomicWrite(s.checkpointPath(cp.UpToSeq), []byte(content)); err != nil {
		return fmt.Errorf("watch: write checkpoint context package: %w", err)
	}
	raw, err := json.Marshal(cursorPayload{UpToSeq: cp.UpToSeq})
	if err != nil {
		return fmt.Errorf("watch: marshal cursor: %w", err)
	}
	if err := memory.AtomicWrite(filepath.Join(s.dir, cursorFileName), raw); err != nil {
		return fmt.Errorf("watch: advance cursor: %w", err)
	}
	return nil
}

// ReadCheckpoint reads back a previously committed checkpoint's context package — "", false if it was never
// committed (missing log — not verified) or cannot be read.
func (s *CheckpointStore) ReadCheckpoint(upToSeq int) (string, bool) {
	raw, err := os.ReadFile(s.checkpointPath(upToSeq))
	if err != nil {
		return "", false
	}
	return string(raw), true
}
