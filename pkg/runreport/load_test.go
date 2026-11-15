package runreport

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/agentjobs"
	"github.com/akopichin/afm/contracts/state"
)

var fixtureStart = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

// writeEvents writes a run whose stages go pending -> running -> done, one second apart.
func writeEvents(t *testing.T, runDir string, stages ...string) {
	t.Helper()
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var data []byte
	seq := uint64(0)
	at := fixtureStart
	for _, id := range stages {
		for _, step := range [][2]state.StageStatus{{state.StatusPending, state.StatusRunning}, {state.StatusRunning, state.StatusDone}} {
			seq++
			at = at.Add(time.Second)
			line, err := json.Marshal(state.Transition{Seq: seq, Time: at, StageID: id, From: step[0], To: step[1], Event: "test"})
			if err != nil {
				t.Fatal(err)
			}
			data = append(append(data, line...), '\n')
		}
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeUsage(t *testing.T, dir string, recs ...accounting.UsageRecord) {
	t.Helper()
	var data []byte
	for _, rec := range recs {
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		data = append(append(data, line...), '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "usage.jsonl"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeJobs journals one finished job through the real store, the only writer of the format.
func writeJobs(t *testing.T, dir string, metas ...agentjobs.JobMeta) {
	t.Helper()
	store, err := agentjobs.Open(dir, agentjobs.Header{RecordingStartedAt: fixtureStart})
	if err != nil {
		t.Fatal(err)
	}
	for i, meta := range metas {
		at := fixtureStart.Add(time.Duration(i+1) * time.Second)
		if err := store.Append(agentjobs.Record{Type: agentjobs.TypeStarted, JobMeta: meta, At: at}); err != nil {
			t.Fatal(err)
		}
		fin := agentjobs.Record{Type: agentjobs.TypeFinished, JobMeta: agentjobs.JobMeta{JobID: meta.JobID}, At: at.Add(time.Second), Outcome: agentjobs.OutcomeOK, DurationMS: 1000}
		if err := store.Append(fin); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func appendRaw(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func treeHash(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(path) //nolint:gosec // read-only walk of a test temp dir
		out[path] = sha256.Sum256(data)
		return rerr
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// newAfmRoot returns <afm>/runs/<id> with its events written and the sibling side-agent dir created.
func newAfmRoot(t *testing.T, id string, stages ...string) (afm, runDir, sideDir string) {
	t.Helper()
	afm = t.TempDir()
	runDir = filepath.Join(afm, "runs", id)
	sideDir = filepath.Join(afm, "side-agent", id)
	writeEvents(t, runDir, stages...)
	if err := os.MkdirAll(sideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return afm, runDir, sideDir
}

func TestLoadReadsEverySourceAtOneAsOf(t *testing.T) {
	afm, runDir, sideDir := newAfmRoot(t, "flow-20261101-100000", "build", "test")
	writeUsage(t, runDir, accounting.UsageRecord{RecordVersion: 1, StageID: "build", Metered: true})
	writeJobs(t, runDir, agentjobs.JobMeta{JobID: "j1", StageID: "build", AgentID: "stage:build"})
	writeUsage(t, sideDir, accounting.UsageRecord{RecordVersion: 1, Metered: true})
	writeJobs(t, sideDir, agentjobs.JobMeta{JobID: "a1", AgentID: "side:flow-20261101-100000"})

	now := time.Date(2026, 11, 2, 0, 0, 0, 123456789, time.FixedZone("x", 3600))
	src, err := Load(runDir, LoadOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	if want := time.Date(2026, 11, 1, 23, 0, 0, 123000000, time.UTC); !src.AsOf.Equal(want) || src.AsOf.Location() != time.UTC {
		t.Errorf("AsOf = %v, want %v in UTC truncated to ms", src.AsOf, want)
	}
	if src.RunID != "flow-20261101-100000" || src.Assistant.Dir != filepath.Join(afm, "side-agent", src.RunID) {
		t.Errorf("run id %q, assistant dir %q", src.RunID, src.Assistant.Dir)
	}
	for name, status := range map[string]SourceStatus{
		"usage": src.Usage.Status, "jobs": src.Jobs.Status,
		"assistant usage": src.Assistant.Usage.Status, "assistant jobs": src.Assistant.Journal.Status,
	} {
		if status.State != SourceOK {
			t.Errorf("%s status = %+v, want ok", name, status)
		}
	}
	if got := src.FirstStart["build"]; !got.Equal(fixtureStart.Add(time.Second)) {
		t.Errorf("first start of build = %v", got)
	}
	if len(src.Usage.Records) != 1 || len(src.Jobs.Snapshot.Records) != 2 || len(src.Assistant.Journal.Snapshot.Records) != 2 {
		t.Errorf("records: usage %d, jobs %d, assistant jobs %d", len(src.Usage.Records), len(src.Jobs.Snapshot.Records), len(src.Assistant.Journal.Snapshot.Records))
	}
}

func TestLoadTornTailIsNeitherFixedNorCorrupt(t *testing.T) {
	_, runDir, sideDir := newAfmRoot(t, "flow-20261101-100000", "build")
	writeUsage(t, runDir, accounting.UsageRecord{RecordVersion: 1, StageID: "build", Metered: true})
	writeJobs(t, runDir, agentjobs.JobMeta{JobID: "j1", StageID: "build"})
	writeJobs(t, sideDir, agentjobs.JobMeta{JobID: "a1"})
	for _, name := range []string{"events.jsonl", "usage.jsonl", agentjobs.FileName} {
		appendRaw(t, filepath.Join(runDir, name), `{"torn":`)
	}
	appendRaw(t, filepath.Join(sideDir, agentjobs.FileName), `{"torn":`)
	afm := filepath.Dir(filepath.Dir(runDir))
	before := treeHash(t, afm)

	src, err := Load(runDir, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if src.Usage.Status.State != SourceOK || src.Jobs.Status.State != SourceOK || src.Assistant.Journal.Status.State != SourceOK {
		t.Errorf("a torn tail must stay ok: usage %+v jobs %+v assistant %+v", src.Usage.Status, src.Jobs.Status, src.Assistant.Journal.Status)
	}
	assertTreeUnchanged(t, afm, before)
}

func assertTreeUnchanged(t *testing.T, root string, before map[string][32]byte) {
	t.Helper()
	after := treeHash(t, root)
	if len(after) != len(before) {
		t.Fatalf("files under %s: %d before, %d after", root, len(before), len(after))
	}
	for path, sum := range before {
		if after[path] != sum {
			t.Errorf("%s changed", path)
		}
	}
}

func TestLoadStatusesOfBrokenAndMissingSources(t *testing.T) {
	_, runDir, _ := newAfmRoot(t, "flow-20261101-100000", "build")
	if err := os.WriteFile(filepath.Join(runDir, "usage.jsonl"), []byte("not json\n{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A conflicting second started line is a degraded journal, not a missing one.
	writeJobs(t, runDir, agentjobs.JobMeta{JobID: "j1"})
	appendRaw(t, filepath.Join(runDir, agentjobs.FileName),
		`{"type":"started","job_id":"j1","agent_id":"other","at":"2026-11-01T10:00:09Z"}`+"\n")

	src, err := Load(runDir, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if src.Usage.Status.State != SourceCorrupt || src.Usage.Err == nil {
		t.Errorf("usage = %+v / %v, want corrupt with an error", src.Usage.Status, src.Usage.Err)
	}
	if src.Jobs.Status.State != SourceDegraded || src.Jobs.Status.Reason != agentjobs.ReasonConflictingRecord {
		t.Errorf("jobs = %+v, want degraded conflicting_record", src.Jobs.Status)
	}
}

func TestLoadLegacyRunHasAbsentSources(t *testing.T) {
	_, runDir, _ := newAfmRoot(t, "flow-20261101-100000", "build")
	// A run kept outside <afm>/runs has no Assistant directory to look in.
	elsewhere := filepath.Join(t.TempDir(), "copy")
	if err := os.CopyFS(elsewhere, os.DirFS(runDir)); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{runDir, elsewhere} {
		src, err := Load(dir, LoadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if src.Usage.Status.State != SourceAbsent || src.Jobs.Status.State != SourceAbsent || src.Usage.Err != nil {
			t.Errorf("%s: usage %+v jobs %+v", dir, src.Usage.Status, src.Jobs.Status)
		}
		if src.Assistant.Dir != "" && dir == elsewhere {
			t.Errorf("assistant must not be probed for a run outside runs/: %+v", src.Assistant)
		}
	}
}

func TestLoadFailsOnUnreplayableState(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "runs", "r")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), []byte("garbage\n{\"seq\":1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(runDir, LoadOptions{}); err == nil {
		t.Fatal("expected a load state error")
	}
}
