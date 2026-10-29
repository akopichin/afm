package stagefiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// act appends one assistant_activity notice the way production does.
func act(runDir, task, detail string) {
	AppendNotice(runDir, "stage-a", "assistant_activity", ActivityData{
		EventID: "act-" + detail, TaskID: task, ParticipantID: "p-" + task, Turn: 1,
		OccurredAt: "2026-10-27T10:00:00Z", Tool: "Bash", Detail: detail,
	})
}

// appendRaw writes bytes to notices.jsonl verbatim (corrupt, foreign or unfinished lines).
func appendRaw(t *testing.T, runDir, raw string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(runDir, "notices.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(raw); err != nil {
		t.Fatal(err)
	}
}

func details(p ActivityPage) []string {
	out := make([]string, 0, len(p.Entries))
	for _, e := range p.Entries {
		out = append(out, e.Detail)
	}
	return out
}

func u64(v uint64) *uint64 { return &v }

func mustRead(t *testing.T, runDir, task string, q ActivityQuery) ActivityPage {
	t.Helper()
	p, err := ReadTaskActivity(runDir, task, q)
	if err != nil {
		t.Fatalf("ReadTaskActivity(%+v): %v", q, err)
	}
	return p
}

func wantDetails(t *testing.T, p ActivityPage, want ...string) {
	t.Helper()
	if got := details(p); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("details = %v, want %v", got, want)
	}
}

func TestReadTaskActivity_MissingFileIsEmptyHistory(t *testing.T) {
	p := mustRead(t, t.TempDir(), "t-1", ActivityQuery{Limit: 10})
	if p.Entries == nil || len(p.Entries) != 0 || p.After != 0 || p.Before != 0 || p.HasMore || p.HasOlder {
		t.Fatalf("page = %+v, want an empty non-nil page", p)
	}
}

func TestReadTaskActivity_TailFiltersTaskAndTypeInFileOrder(t *testing.T) {
	dir := t.TempDir()
	act(dir, "t-1", "one")
	act(dir, "t-2", "other task")
	AppendNotice(dir, "stage-a", "verify_started", map[string]any{"task_id": "t-1"})
	act(dir, "t-1", "two")
	act(dir, "t-1", "two") // identical content stays twice

	p := mustRead(t, dir, "t-1", ActivityQuery{Limit: 10})
	wantDetails(t, p, "one", "two", "two")
	if p.HasOlder || p.HasMore {
		t.Errorf("has_older/has_more = %v/%v, want false/false", p.HasOlder, p.HasMore)
	}
	if p.Entries[0].StageID != "stage-a" || p.Entries[0].EventID != "act-one" || p.Entries[0].ParticipantID != "p-t-1" {
		t.Errorf("entry = %+v", p.Entries[0])
	}
	fi, _ := os.Stat(filepath.Join(dir, "notices.jsonl"))
	if p.After != uint64(fi.Size()) || p.Entries[2].Cursor != p.After {
		t.Errorf("after = %d, last cursor = %d, file size = %d", p.After, p.Entries[2].Cursor, fi.Size())
	}
	if p.Entries[0].Cursor >= p.Entries[1].Cursor {
		t.Errorf("cursors must grow: %d, %d", p.Entries[0].Cursor, p.Entries[1].Cursor)
	}
}

func TestReadTaskActivity_TailKeepsNewestLimitAndReportsOlder(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 5; i++ {
		act(dir, "t-1", fmt.Sprint("e", i))
	}
	p := mustRead(t, dir, "t-1", ActivityQuery{Limit: 2})
	wantDetails(t, p, "e4", "e5")
	if !p.HasOlder {
		t.Error("has_older = false, want true")
	}
	// before is where the first returned entry starts: paging with it must return exactly the older ones.
	older := mustRead(t, dir, "t-1", ActivityQuery{Limit: 2, Before: u64(p.Before)})
	wantDetails(t, older, "e2", "e3")
	if !older.HasOlder {
		t.Error("has_older = false, want true (e1 remains)")
	}
	oldest := mustRead(t, dir, "t-1", ActivityQuery{Limit: 2, Before: u64(older.Before)})
	wantDetails(t, oldest, "e1")
	if oldest.HasOlder {
		t.Error("has_older = true at the start of history")
	}
}

func TestReadTaskActivity_AfterPagesForwardAndSkipsOtherLines(t *testing.T) {
	dir := t.TempDir()
	act(dir, "t-1", "a")
	first := mustRead(t, dir, "t-1", ActivityQuery{Limit: 10})
	act(dir, "t-2", "noise")
	AppendNotice(dir, "stage-a", "quota_wait", "x")
	for _, d := range []string{"b", "c", "d"} {
		act(dir, "t-1", d)
	}
	act(dir, "t-2", "trailing noise")

	p := mustRead(t, dir, "t-1", ActivityQuery{Limit: 2, After: u64(first.After)})
	wantDetails(t, p, "b", "c")
	if !p.HasMore {
		t.Error("has_more = false, want true after a full page")
	}
	rest := mustRead(t, dir, "t-1", ActivityQuery{Limit: 2, After: u64(p.After)})
	wantDetails(t, rest, "d")
	if rest.HasMore {
		t.Error("has_more = true on the last page")
	}
	fi, _ := os.Stat(filepath.Join(dir, "notices.jsonl"))
	if rest.After != uint64(fi.Size()) {
		t.Errorf("after = %d, want the end of the file %d so the next poll reads only new bytes", rest.After, fi.Size())
	}
	empty := mustRead(t, dir, "t-1", ActivityQuery{Limit: 2, After: u64(rest.After)})
	if len(empty.Entries) != 0 || empty.After != rest.After {
		t.Errorf("empty poll = %+v, want no entries and the same after", empty)
	}
}

func TestReadTaskActivity_UnfinishedTailIsNotConsumedUntilCompleted(t *testing.T) {
	dir := t.TempDir()
	act(dir, "t-1", "done")
	first := mustRead(t, dir, "t-1", ActivityQuery{Limit: 10})

	partial := `{"time":"2026-10-27T10:00:00Z","type":"assistant_activity","stage_id":"s","data":{"event_id":"act-late","task_id":"t-1","detail":"late"`
	appendRaw(t, dir, partial)

	p := mustRead(t, dir, "t-1", ActivityQuery{Limit: 10, After: u64(first.After)})
	if len(p.Entries) != 0 || p.After != first.After {
		t.Fatalf("page = %+v, want nothing and the cursor held before the unfinished line", p)
	}
	tail := mustRead(t, dir, "t-1", ActivityQuery{Limit: 10})
	wantDetails(t, tail, "done")

	appendRaw(t, dir, "}}\n")
	p = mustRead(t, dir, "t-1", ActivityQuery{Limit: 10, After: u64(first.After)})
	wantDetails(t, p, "late")
}

func TestReadTaskActivity_SkipsCorruptAndOverlongLinesWithoutLosingLaterOnes(t *testing.T) {
	dir := t.TempDir()
	act(dir, "t-1", "before")
	appendRaw(t, dir, "not json at all\n")
	appendRaw(t, dir, `{"type":"assistant_activity","data":"oops"}`+"\n")
	appendRaw(t, dir, `{"type":"assistant_activity","data":{"task_id":"t-1","detail":"`+strings.Repeat("x", 2<<20)+"\"}}\n")
	act(dir, "t-1", "after")

	for _, q := range []ActivityQuery{{Limit: 10}, {Limit: 10, After: u64(0)}} {
		wantDetails(t, mustRead(t, dir, "t-1", q), "before", "after")
	}
}

func TestReadTaskActivity_EntryWithoutEventIDIsNotAValidRow(t *testing.T) {
	dir := t.TempDir()
	appendRaw(t, dir, `{"type":"assistant_activity","stage_id":"s","data":{"task_id":"t-1","detail":"anonymous"}}`+"\n")
	act(dir, "t-1", "real")
	wantDetails(t, mustRead(t, dir, "t-1", ActivityQuery{Limit: 10}), "real")
}

func TestReadTaskActivity_RejectsBadCursors(t *testing.T) {
	dir := t.TempDir()
	act(dir, "t-1", "a")
	act(dir, "t-1", "b")
	p := mustRead(t, dir, "t-1", ActivityQuery{Limit: 10})

	cases := map[string]ActivityQuery{
		"mid-line after":   {Limit: 10, After: u64(5)},
		"mid-line before":  {Limit: 10, Before: u64(p.Entries[0].Cursor - 1)},
		"past end of file": {Limit: 10, After: u64(p.After + 1)},
		"before past end":  {Limit: 10, Before: u64(p.After + 100)},
		"after and before": {Limit: 10, After: u64(0), Before: u64(0)},
		"huge after":       {Limit: 10, After: u64(1 << 62)},
	}
	for name, q := range cases {
		if _, err := ReadTaskActivity(dir, "t-1", q); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%s: err = %v, want ErrInvalidCursor", name, err)
		}
	}
	// Offset 0 and the exact end are valid boundaries.
	mustRead(t, dir, "t-1", ActivityQuery{Limit: 10, After: u64(0)})
	mustRead(t, dir, "t-1", ActivityQuery{Limit: 10, Before: u64(p.After)})
}

func TestReadTaskActivity_CursorOnMissingFile(t *testing.T) {
	dir := t.TempDir()
	mustRead(t, dir, "t-1", ActivityQuery{Limit: 10, After: u64(0)})
	if _, err := ReadTaskActivity(dir, "t-1", ActivityQuery{Limit: 10, After: u64(7)}); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("err = %v, want ErrInvalidCursor", err)
	}
}

func TestReadTaskActivity_BeforeStartsAtOffsetZeroEmpty(t *testing.T) {
	dir := t.TempDir()
	act(dir, "t-1", "a")
	p := mustRead(t, dir, "t-1", ActivityQuery{Limit: 10, Before: u64(0)})
	if len(p.Entries) != 0 || p.HasOlder {
		t.Fatalf("page = %+v, want empty", p)
	}
}

// A reader racing a writer sees only whole lines: every page it gets is a prefix of the final history and its
// cursor chains into the next read without gaps or repeats.
func TestReadTaskActivity_ReaderRacingAppenderSeesOnlyWholeOrderedLines(t *testing.T) {
	dir := t.TempDir()
	const total = 200
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range total {
			act(dir, "t-1", fmt.Sprint("e", i))
		}
	}()

	var got []string
	var after uint64
	for len(got) < total {
		p := mustRead(t, dir, "t-1", ActivityQuery{Limit: 7, After: u64(after)})
		got = append(got, details(p)...)
		after = p.After
	}
	<-done
	for i, d := range got {
		if d != fmt.Sprint("e", i) {
			t.Fatalf("entry %d = %q, want e%d (gap or repeat across pages)", i, d, i)
		}
	}
}

func TestReadTaskActivity_LimitIsDefaultedAndCapped(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < MaxActivityLimit+DefaultActivityLimit; i++ {
		act(dir, "t-1", fmt.Sprint("e", i))
	}
	if got := len(mustRead(t, dir, "t-1", ActivityQuery{}).Entries); got != DefaultActivityLimit {
		t.Errorf("default page = %d, want %d", got, DefaultActivityLimit)
	}
	if got := len(mustRead(t, dir, "t-1", ActivityQuery{Limit: 10 * MaxActivityLimit}).Entries); got != MaxActivityLimit {
		t.Errorf("capped page = %d, want %d", got, MaxActivityLimit)
	}
}

func TestActivityPage_WireShapeIsFlat(t *testing.T) {
	dir := t.TempDir()
	act(dir, "t-1", "a")
	raw, err := json.Marshal(mustRead(t, dir, "t-1", ActivityQuery{Limit: 1}))
	if err != nil {
		t.Fatal(err)
	}
	var page map[string]any
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"entries", "after", "before", "has_more", "has_older"} {
		if _, ok := page[k]; !ok {
			t.Errorf("page lacks %q: %s", k, raw)
		}
	}
	entry := page["entries"].([]any)[0].(map[string]any)
	for _, k := range []string{"event_id", "task_id", "participant_id", "turn", "occurred_at", "tool", "detail", "stage_id", "cursor"} {
		if _, ok := entry[k]; !ok {
			t.Errorf("entry lacks %q: %s", k, raw)
		}
	}
	if _, has := entry["truncated"]; has {
		t.Errorf("a whole entry must not carry truncated: %s", raw)
	}
}

func TestReadTaskActivity_AfterPageWithExactlyLimitEntriesHasNoMore(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"a", "b", "c"} {
		act(dir, "t-1", d)
	}
	act(dir, "t-2", "other task after the last match")

	p := mustRead(t, dir, "t-1", ActivityQuery{Limit: 3, After: u64(0)})
	wantDetails(t, p, "a", "b", "c")
	if p.HasMore {
		t.Error("has_more = true with exactly limit matching entries and no further match")
	}

	act(dir, "t-1", "d")
	p = mustRead(t, dir, "t-1", ActivityQuery{Limit: 3, After: u64(0)})
	wantDetails(t, p, "a", "b", "c")
	if !p.HasMore {
		t.Error("has_more = false although another matching entry follows")
	}
	if rest := mustRead(t, dir, "t-1", ActivityQuery{Limit: 3, After: u64(p.After)}); len(rest.Entries) != 1 {
		t.Errorf("continuing from after returned %v, want only d", details(rest))
	}
}

func TestReadTaskActivity_TailAfterStaysAtLastCompleteLine(t *testing.T) {
	dir := t.TempDir()
	act(dir, "t-1", "done")
	act(dir, "t-2", "other task")
	fi, _ := os.Stat(filepath.Join(dir, "notices.jsonl"))
	appendRaw(t, dir, `{"type":"assistant_activity","data":{"event_id":"act-x","task_id":"t-1"`)

	p := mustRead(t, dir, "t-1", ActivityQuery{Limit: 10})
	wantDetails(t, p, "done")
	if p.After != uint64(fi.Size()) {
		t.Errorf("after = %d, want %d: the end of the last complete line, not the unfinished one", p.After, fi.Size())
	}
}

func TestReadTaskActivity_TailAfterOfTaskWithoutHistoryIsTheEndOfExaminedLines(t *testing.T) {
	dir := t.TempDir()
	act(dir, "t-2", "somebody else")
	AppendNotice(dir, "s", "quota_wait", "x")
	fi, _ := os.Stat(filepath.Join(dir, "notices.jsonl"))

	p := mustRead(t, dir, "t-1", ActivityQuery{Limit: 10})
	if len(p.Entries) != 0 || p.After != uint64(fi.Size()) {
		t.Fatalf("page = %+v, want no entries and after = %d so the first poll skips what was already examined", p, fi.Size())
	}
	act(dir, "t-1", "first")
	wantDetails(t, mustRead(t, dir, "t-1", ActivityQuery{Limit: 10, After: u64(p.After)}), "first")
}

// The largest legal entry — a 16 KiB detail that every byte of JSON-escapes to six bytes, plus a capped tool —
// must still fit the reader's line buffer, or the action would vanish after a reload.
func TestReadTaskActivity_MaxSizeEscapedEntryReadsBack(t *testing.T) {
	dir := t.TempDir()
	// "<", a control byte and invalid UTF-8 all encode to six bytes of JSON.
	detail := strings.Repeat("<", 4096) + strings.Repeat("\x01", 4096) + strings.Repeat("\xff", 4096) + strings.Repeat("\xfe", 4096)
	tool := strings.Repeat("<", MaxActivityToolBytes)
	AppendNotice(dir, "stage-a", "assistant_activity", ActivityData{
		EventID: "act-max", TaskID: "t-1", ParticipantID: "p", Turn: 1, OccurredAt: "2026-10-27T10:00:00Z",
		Tool: tool, Detail: detail, Truncated: true,
	})
	p := mustRead(t, dir, "t-1", ActivityQuery{Limit: 10})
	if len(p.Entries) != 1 {
		t.Fatalf("entries = %d, want the max-size row to be read back", len(p.Entries))
	}
}

func TestReadTaskActivity_ForeignTypeLineWithActivityMarkerIsNotReturned(t *testing.T) {
	dir := t.TempDir()
	appendRaw(t, dir, `{"type":"verify_result","stage_id":"s","data":{"event_id":"act-f","task_id":"t-1","detail":"assistant_activity"}}`+"\n")
	act(dir, "t-1", "real")
	wantDetails(t, mustRead(t, dir, "t-1", ActivityQuery{Limit: 10}), "real")
	wantDetails(t, mustRead(t, dir, "t-1", ActivityQuery{Limit: 10, After: u64(0)}), "real")
}

// The raw task_id prefilter must never drop a valid row: ids that contain each other, and detail text that
// quotes another task's id, are told apart only by the decoded field.
func TestReadTaskActivity_TaskIDFilterIsExactNotSubstring(t *testing.T) {
	dir := t.TempDir()
	act(dir, "t-1", "short")
	act(dir, "t-10", "suffix-longer")
	act(dir, "xt-1", "prefix-longer")
	act(dir, "t-2", `mentions "task_id":"t-1" in text`)
	act(dir, "t-1", `mentions "task_id":"t-2" in text`)

	wantDetails(t, mustRead(t, dir, "t-1", ActivityQuery{Limit: 10}), "short", `mentions "task_id":"t-2" in text`)
	wantDetails(t, mustRead(t, dir, "t-1", ActivityQuery{Limit: 10, After: u64(0)}), "short", `mentions "task_id":"t-2" in text`)
	wantDetails(t, mustRead(t, dir, "t-2", ActivityQuery{Limit: 10}), `mentions "task_id":"t-1" in text`)
}

func TestIsActivityLine(t *testing.T) {
	for line, want := range map[string]bool{
		`{"type":"assistant_activity","data":{}}`:                         true,
		`{"type":"verify_result","data":{"detail":"assistant_activity"}}`: false,
		`{"type":"agent_completed","data":"\"assistant_activity\""}`:      false,
		`not json "assistant_activity"`:                                   false,
		`{"type":"verify_started"}`:                                       false,
	} {
		if got := IsActivityLine([]byte(line)); got != want {
			t.Errorf("IsActivityLine(%s) = %v, want %v", line, got, want)
		}
	}
}
