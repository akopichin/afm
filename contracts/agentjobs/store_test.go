package agentjobs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

var testStart = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func testHeader() Header {
	return Header{RecordingStartedAt: testStart, BaseOffsetMS: 4200}
}

func startedRecord(jobID string) Record {
	return Record{
		Type:    TypeStarted,
		JobMeta: JobMeta{JobID: jobID, AgentID: "stage:build", AgentKind: "stage", StageID: "build", Title: "Build · plan"},
		At:      testStart.Add(time.Second),
	}
}

func finishedRecord(jobID string) Record {
	return Record{
		Type:       TypeFinished,
		JobMeta:    JobMeta{JobID: jobID},
		At:         testStart.Add(3 * time.Second),
		Outcome:    OutcomeOK,
		DurationMS: 2000,
	}
}

func openStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir, testHeader())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustAppend(t *testing.T, s *Store, recs ...Record) {
	t.Helper()
	for _, rec := range recs {
		if err := s.Append(rec); err != nil {
			t.Fatalf("Append(%s %s): %v", rec.Type, rec.JobID, err)
		}
	}
}

func journalPath(dir string) string { return filepath.Join(dir, FileName) }

func readFile(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(journalPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeFile(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(journalPath(dir), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lineCount(t *testing.T, dir string) int {
	t.Helper()
	return strings.Count(readFile(t, dir), "\n")
}

func hasReason(h Health, reason string) bool { return slices.Contains(h.Reasons, reason) }

// headerLine and recordLine build raw journal lines without going through Store.
func headerLine(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	openStore(t, dir)
	return readFile(t, dir)
}

func recordLine(t *testing.T, rec Record) string {
	t.Helper()
	dir := t.TempDir()
	s := openStore(t, dir)
	mustAppend(t, s, rec)
	return strings.SplitAfter(readFile(t, dir), "\n")[1]
}

func TestOpenWritesHeaderOnce(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)

	snap := s.Snapshot()
	if snap.Header.RecordVersion != RecordVersion || snap.Header.Type != TypeHeader {
		t.Fatalf("header = %+v", snap.Header)
	}
	if !snap.Header.RecordingStartedAt.Equal(testStart) || snap.Header.BaseOffsetMS != 4200 {
		t.Fatalf("header = %+v", snap.Header)
	}
	if snap.Health.Status != HealthOK || len(snap.Records) != 0 || snap.Revision != 0 {
		t.Fatalf("fresh snapshot = %+v", snap)
	}
	if got := lineCount(t, dir); got != 1 {
		t.Fatalf("fresh file has %d lines, want only the header", got)
	}
}

func TestReopenKeepsHeaderAndRecords(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	mustAppend(t, s, startedRecord("job-1"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, dir)

	other := Header{RecordingStartedAt: testStart.Add(time.Hour), BaseOffsetMS: 99}
	reopened, err := Open(dir, other)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	snap := reopened.Snapshot()
	if !snap.Header.RecordingStartedAt.Equal(testStart) || snap.Header.BaseOffsetMS != 4200 {
		t.Fatalf("header was rewritten: %+v", snap.Header)
	}
	if len(snap.Records) != 1 || snap.Records[0].JobID != "job-1" {
		t.Fatalf("records = %+v", snap.Records)
	}
	if after := readFile(t, dir); after != before {
		t.Fatalf("reopen changed the file:\n%s\n--- vs ---\n%s", before, after)
	}
}

func TestAppendedRecordsRoundTripThroughLoad(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	full := Record{
		Type: TypeStarted,
		JobMeta: JobMeta{
			JobID: "job-1", RunID: "run-1", AgentID: "helper:t1", AgentKind: "helper",
			StageID: "build", RootStageID: "build", TaskID: "t1", ParentTaskID: "t0",
			ProfileID: "dev", Phase: "implement", VerifyStep: 2, Turn: 3, Attempt: 2,
			SessionID: "sess-1", PrevJobID: "job-0", Title: "Helper · t1",
			Channel: "claude", Model: "claude-opus-4-8",
		},
		At:      testStart.Add(time.Second),
		Segment: Segment{RunStartedAt: testStart, BaseMS: 1500, OffsetMS: 2500},
	}
	failed := Record{
		Type: TypeStartFailed, JobMeta: JobMeta{JobID: "job-2", StageID: "build"},
		At: testStart.Add(2 * time.Second), Reason: "start_error",
	}
	mustAppend(t, s, full, failed, finishedRecord("job-1"))

	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	live := s.Snapshot()
	if loaded.Revision != live.Revision || loaded.Revision != 3 {
		t.Fatalf("revisions: load %d, live %d, want 3", loaded.Revision, live.Revision)
	}
	if len(loaded.Records) != 3 {
		t.Fatalf("loaded %d records", len(loaded.Records))
	}
	got := loaded.Records[0]
	if got.JobMeta != full.JobMeta || !got.At.Equal(full.At) || !got.Segment.RunStartedAt.Equal(testStart) ||
		got.Segment.BaseMS != 1500 || got.Segment.OffsetMS != 2500 {
		t.Fatalf("started round trip = %+v", got)
	}
	if loaded.Records[1].Reason != "start_error" || loaded.Records[2].Outcome != OutcomeOK || loaded.Records[2].DurationMS != 2000 {
		t.Fatalf("records = %+v", loaded.Records[1:])
	}
}

func TestSnapshotIsImmutable(t *testing.T) {
	s := openStore(t, t.TempDir())
	// Three records leave spare capacity in the store's backing array (cap 4).
	mustAppend(t, s, startedRecord("job-1"), startedRecord("job-2"), startedRecord("job-3"))

	first := s.Snapshot()
	mustAppend(t, s, finishedRecord("job-1"))
	if len(first.Records) != 3 {
		t.Fatalf("old snapshot grew to %d records", len(first.Records))
	}

	// Appending to the caller's copy must not overwrite the record the store just added.
	_ = append(first.Records, startedRecord("intruder"))
	second := s.Snapshot()
	if len(second.Records) != 4 || second.Records[3].Type != TypeFinished {
		t.Fatalf("store records were changed through a snapshot: %+v", second.Records)
	}
}

func TestIdenticalRepeatIsDeduplicated(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)

	rec := startedRecord("job-1")
	rec.At = time.Now() // carries a monotonic reading and a local zone
	mustAppend(t, s, rec)
	revision := s.Snapshot().Revision
	lines := lineCount(t, dir)

	mustAppend(t, s, rec)
	if got := s.Snapshot(); got.Revision != revision || len(got.Records) != 1 || lineCount(t, dir) != lines {
		t.Fatalf("repeat changed state: revision %d→%d, records %d, lines %d→%d",
			revision, got.Revision, len(got.Records), lines, lineCount(t, dir))
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openStore(t, dir)
	mustAppend(t, reopened, rec)
	if lineCount(t, dir) != lines {
		t.Fatal("repeat after reopen was written again")
	}
}

func TestConflictingRepeatIsDiagnosedNotApplied(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	mustAppend(t, s, startedRecord("job-1"))
	revision := s.Snapshot().Revision
	lines := lineCount(t, dir)

	conflicting := startedRecord("job-1")
	conflicting.Title = "something else"
	if err := s.Append(conflicting); !errors.Is(err, ErrConflictingRecord) {
		t.Fatalf("Append(conflict) = %v, want ErrConflictingRecord", err)
	}

	snap := s.Snapshot()
	if snap.Records[0].Title != "Build · plan" || len(snap.Records) != 1 || lineCount(t, dir) != lines {
		t.Fatalf("conflict replaced or added a record: %+v", snap.Records)
	}
	if snap.Health.Status != HealthDegraded || !hasReason(snap.Health, ReasonConflictingRecord) {
		t.Fatalf("health = %+v", snap.Health)
	}
	if snap.Revision != revision+1 {
		t.Fatalf("revision %d→%d, want the health change to bump it", revision, snap.Revision)
	}

	// A degraded journal keeps accepting new facts.
	mustAppend(t, s, finishedRecord("job-1"))
	if len(s.Snapshot().Records) != 2 {
		t.Fatal("append after conflict was refused")
	}
}

func TestStartedAndStartFailedForOneJobConflict(t *testing.T) {
	s := openStore(t, t.TempDir())
	mustAppend(t, s, startedRecord("job-1"))

	failed := Record{Type: TypeStartFailed, JobMeta: JobMeta{JobID: "job-1"}, At: testStart, Reason: "start_error"}
	if err := s.Append(failed); !errors.Is(err, ErrConflictingRecord) {
		t.Fatalf("Append(start_failed after started) = %v", err)
	}

	s2 := openStore(t, t.TempDir())
	mustAppend(t, s2, failed)
	if err := s2.Append(startedRecord("job-1")); !errors.Is(err, ErrConflictingRecord) {
		t.Fatalf("Append(started after start_failed) = %v", err)
	}
}

func TestLoadIgnoresTornTailAndNeverWrites(t *testing.T) {
	dir := t.TempDir()
	content := headerLine(t) + recordLine(t, startedRecord("job-1")) + `{"type":"finished","job_id":"job-1","at":"2026-10`
	writeFile(t, dir, content)

	snap, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Records) != 1 || snap.Health.Status != HealthOK {
		t.Fatalf("snapshot = %+v", snap)
	}
	if got := readFile(t, dir); got != content {
		t.Fatal("Load modified the file")
	}
}

func TestOpenRepairsTornTailBeforeAppend(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, headerLine(t)+recordLine(t, startedRecord("job-1"))+`{"type":"finished","job_id":"jo`)

	s := openStore(t, dir)
	mustAppend(t, s, finishedRecord("job-1"))

	for i, line := range strings.Split(strings.TrimSuffix(readFile(t, dir), "\n"), "\n") {
		if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
			t.Fatalf("line %d is not a complete record: %q", i, line)
		}
	}
	snap, err := Load(dir)
	if err != nil || len(snap.Records) != 2 || snap.Health.Status != HealthOK {
		t.Fatalf("after repair: %+v, %v", snap, err)
	}
}

func TestCorruptInteriorLineIsSkippedAndDegradesHealth(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, headerLine(t)+"{garbage\n"+recordLine(t, startedRecord("job-1")))

	snap, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Records) != 1 || snap.Health.Status != HealthDegraded || !hasReason(snap.Health, ReasonCorruptLine) {
		t.Fatalf("snapshot = %+v", snap)
	}

	s := openStore(t, dir)
	mustAppend(t, s, finishedRecord("job-1"))
	if got := len(s.Snapshot().Records); got != 2 {
		t.Fatalf("records after append = %d", got)
	}
}

func TestUnusableHeaderMakesJournalUnavailable(t *testing.T) {
	validRecord := recordLine(t, startedRecord("job-1"))
	tests := []struct {
		name    string
		content string
		reason  string
	}{
		{"records without header", validRecord, ReasonMissingHeader},
		{"garbage header", "{not json\n" + validRecord, ReasonCorruptHeader},
		{"header without start time", `{"type":"header","record_version":1}` + "\n" + validRecord, ReasonCorruptHeader},
		{"future version", `{"type":"header","record_version":2,"recording_started_at":"2026-10-31T12:00:00Z"}` + "\n" + validRecord, ReasonUnsupportedVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, tt.content)

			snap, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if snap.Health.Status != HealthUnavailable || !hasReason(snap.Health, tt.reason) || len(snap.Records) != 0 {
				t.Fatalf("Load = %+v, want unavailable with %s", snap, tt.reason)
			}

			s, openErr := Open(dir, testHeader())
			if openErr == nil || s == nil {
				t.Fatalf("Open = %v, %v; want a usable unavailable store and an error", s, openErr)
			}
			defer func() { _ = s.Close() }()
			if err := s.Append(startedRecord("job-2")); err == nil {
				t.Fatal("unavailable store accepted a record")
			}
			if s.Snapshot().Health.Status != HealthUnavailable {
				t.Fatalf("health = %+v", s.Snapshot().Health)
			}
			if got := readFile(t, dir); got != tt.content {
				t.Fatal("Open modified an untrusted file")
			}
		})
	}
}

func TestOpenStartsFreshOnFileThatNeverGotItsHeader(t *testing.T) {
	for name, content := range map[string]string{
		"empty":       "",
		"torn header": `{"type":"header","record_ver`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, content)

			s := openStore(t, dir)
			mustAppend(t, s, startedRecord("job-1"))
			snap, err := Load(dir)
			if err != nil || snap.Health.Status != HealthOK || len(snap.Records) != 1 {
				t.Fatalf("Load = %+v, %v", snap, err)
			}
			if !snap.Header.RecordingStartedAt.Equal(testStart) {
				t.Fatalf("header = %+v", snap.Header)
			}
		})
	}
}

func TestLoadWithoutJournalReportsNoJournal(t *testing.T) {
	snap, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load(no file) = %v, want a snapshot", err)
	}
	if snap.Health.Status != HealthUnavailable || !hasReason(snap.Health, ReasonNoJournal) || len(snap.Records) != 0 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestOpenRejectsFreshHeaderWithoutStartTime(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Header{})
	if err == nil || s == nil {
		t.Fatalf("Open = %v, %v", s, err)
	}
	defer func() { _ = s.Close() }()
	if _, statErr := os.Stat(journalPath(dir)); statErr == nil {
		t.Fatal("a journal without a recording start was created")
	}
}

// failingFile injects the write and fsync failures a real disk can produce.
type failingFile struct {
	journalFile
	failWrite, failSync bool
}

func (f failingFile) Write(p []byte) (int, error) {
	if f.failWrite {
		return 0, errors.New("disk full")
	}
	return f.journalFile.Write(p)
}

func (f failingFile) Sync() error {
	if f.failSync {
		return errors.New("fsync failed")
	}
	return f.journalFile.Sync()
}

func TestWriteFailureLatchesUnavailableWithoutLosingSnapshot(t *testing.T) {
	for name, inject := range map[string]func(journalFile) journalFile{
		"write": func(f journalFile) journalFile { return failingFile{journalFile: f, failWrite: true} },
		"fsync": func(f journalFile) journalFile { return failingFile{journalFile: f, failSync: true} },
	} {
		t.Run(name, func(t *testing.T) {
			s := openStore(t, t.TempDir())
			mustAppend(t, s, startedRecord("job-1"))
			before := s.Snapshot()

			s.f = inject(s.f)
			err := s.Append(finishedRecord("job-1"))
			if err == nil {
				t.Fatal("Append succeeded despite the injected failure")
			}

			after := s.Snapshot()
			if len(after.Records) != 1 {
				t.Fatalf("a record that failed to persist is visible: %+v", after.Records)
			}
			if after.Health.Status != HealthUnavailable || !hasReason(after.Health, ReasonIOFailed) {
				t.Fatalf("health = %+v", after.Health)
			}
			if after.Revision != before.Revision+1 {
				t.Fatalf("revision %d→%d: only the health change should bump it", before.Revision, after.Revision)
			}
			if s.Err() == nil {
				t.Fatal("Err() is empty after latching")
			}
			if err := s.Append(startedRecord("job-2")); err == nil {
				t.Fatal("latched store accepted another record")
			}
			if len(before.Records) != 1 || before.Health.Status != HealthOK {
				t.Fatal("snapshot taken before the failure changed")
			}
		})
	}
}

func TestInvalidRecordsAreRejectedWithoutTouchingTheStore(t *testing.T) {
	good := startedRecord("job-1")
	tests := map[string]func(Record) Record{
		"no job id":           func(r Record) Record { r.JobID = ""; return r },
		"no time":             func(r Record) Record { r.At = time.Time{}; return r },
		"unknown type":        func(r Record) Record { r.Type = "paused"; return r },
		"header via append":   func(r Record) Record { r.Type = TypeHeader; return r },
		"finished no outcome": func(r Record) Record { r.Type = TypeFinished; return r },
		"finished bad outcome": func(r Record) Record {
			r.Type, r.Outcome = TypeFinished, "exploded"
			return r
		},
		"finished negative duration": func(r Record) Record {
			r.Type, r.Outcome, r.DurationMS = TypeFinished, OutcomeOK, -1
			return r
		},
		"start_failed no reason": func(r Record) Record { r.Type = TypeStartFailed; return r },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s := openStore(t, dir)
			if err := s.Append(mutate(good)); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("Append = %v, want ErrInvalidRecord", err)
			}
			snap := s.Snapshot()
			if snap.Health.Status != HealthOK || snap.Revision != 0 || lineCount(t, dir) != 1 {
				t.Fatalf("invalid record changed the store: %+v", snap)
			}
		})
	}
}

func TestLongTitleIsClippedOnValidRuneBoundary(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	rec := startedRecord("job-1")
	rec.Title = strings.Repeat("я", MaxTitleLen*3)
	mustAppend(t, s, rec)

	got := s.Snapshot().Records[0].Title
	if n := utf8.RuneCountInString(got); n > MaxTitleLen || !utf8.ValidString(got) {
		t.Fatalf("title has %d runes, valid=%v", n, utf8.ValidString(got))
	}
	loaded, err := Load(dir)
	if err != nil || loaded.Records[0].Title != got {
		t.Fatalf("persisted title differs: %v", err)
	}
}

func TestStartFailedReasonIsClipped(t *testing.T) {
	s := openStore(t, t.TempDir())
	mustAppend(t, s, Record{
		Type: TypeStartFailed, JobMeta: JobMeta{JobID: "job-1"}, At: testStart,
		Reason: strings.Repeat("x", MaxReasonLen*4),
	})
	if got := len(s.Snapshot().Records[0].Reason); got > MaxReasonLen {
		t.Fatalf("reason has %d bytes", got)
	}
}

func TestClosedStoreRefusesAppendButKeepsSnapshot(t *testing.T) {
	s := openStore(t, t.TempDir())
	mustAppend(t, s, startedRecord("job-1"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if err := s.Append(finishedRecord("job-1")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Append after Close = %v", err)
	}
	if len(s.Snapshot().Records) != 1 {
		t.Fatal("snapshot lost its records after Close")
	}
}

func TestConcurrentAppendAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)

	const writers, perWriter = 4, 25
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range perWriter {
				if err := s.Append(startedRecord(fmt.Sprintf("job-%d-%d", w, i))); err != nil {
					t.Errorf("Append: %v", err)
				}
			}
		})
	}
	wg.Go(func() {
		for range 200 {
			snap := s.Snapshot()
			for _, rec := range snap.Records {
				if rec.JobID == "" {
					t.Error("snapshot contains an empty record")
				}
			}
		}
	})
	wg.Wait()

	if got := len(s.Snapshot().Records); got != writers*perWriter {
		t.Fatalf("records = %d, want %d", got, writers*perWriter)
	}
	loaded, err := Load(dir)
	if err != nil || len(loaded.Records) != writers*perWriter || loaded.Health.Status != HealthOK {
		t.Fatalf("Load = %d records, %v, %+v", len(loaded.Records), err, loaded.Health)
	}
}
