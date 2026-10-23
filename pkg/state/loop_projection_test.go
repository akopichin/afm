package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

func openLoopStore(t *testing.T, dir string) *Store {
	t.Helper()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func loopCreatePayload(key string) LoopCreatedPayload {
	return LoopCreatedPayload{
		CreatorID:     agentcomm.UserParticipantID,
		TargetID:      "agent-1",
		Interval:      5 * time.Minute,
		Prompt:        "check the logs",
		OriginStageID: "a",
		ThreadID:      agentcomm.ThreadID("mt-" + key),
	}
}

func loopEffect(t *testing.T, kind, key string, payload any) Effect {
	t.Helper()
	e := Effect{Kind: kind, Target: Target{LoopKey: key}}
	if payload != nil {
		e.Payload = mustMarshal(t, payload)
	}
	return e
}

func applyLoopOp(store *Store, opID string, actor Actor, effects ...Effect) (OperationResult, error) {
	return store.ApplyRunOperation(Operation{ID: opID, Kind: "loop_test", Actor: actor, Effects: effects}, nil)
}

func mustCreateLoop(t *testing.T, store *Store, key string) OperationResult {
	t.Helper()
	res, err := applyLoopOp(store, "create-"+key, Actor{Kind: ActorKindUser}, loopEffect(t, kindLoopCreated, key, loopCreatePayload(key)))
	if err != nil {
		t.Fatalf("create loop %s: %v", key, err)
	}
	return res
}

func mustTickLoop(t *testing.T, store *Store, key string, slot uint64) OperationResult {
	t.Helper()
	res, err := applyLoopOp(store, fmt.Sprintf("tick-%s-%d", key, slot), Actor{Kind: "system"}, loopEffect(t, kindLoopTicked, key, LoopTickedPayload{Slot: slot}))
	if err != nil {
		t.Fatalf("tick loop %s slot %d: %v", key, slot, err)
	}
	return res
}

func requireRejected(t *testing.T, err error, code assistants.ErrorCode) {
	t.Helper()
	var rej *OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != code {
		t.Fatalf("err = %v, want OperationRejectedError with code %s", err, code)
	}
}

func journalSize(t *testing.T, dir string) int64 {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("stat events.jsonl: %v", err)
	}
	return info.Size()
}

func TestLoopCreateTickStopProjection(t *testing.T) {
	dir := t.TempDir()
	store := openLoopStore(t, dir)

	created := mustCreateLoop(t, store, "loop-a")
	l := store.Snapshot().Assistants.Loops["loop-a"]
	if l == nil {
		t.Fatal("loop-a missing from projection")
	}
	if l.ShortID != "L1" || l.Key != "loop-a" {
		t.Errorf("ids = %q/%q, want L1/loop-a", l.ShortID, l.Key)
	}
	if l.CreatorID != agentcomm.UserParticipantID || l.TargetID != "agent-1" || l.Interval != 5*time.Minute ||
		l.Prompt != "check the logs" || l.OriginStageID != "a" || l.ThreadID != "mt-loop-a" {
		t.Errorf("loop fields not recorded: %+v", *l)
	}
	if !l.CreatedAt.Equal(created.Time) || l.CreatedSeq != created.Seq {
		t.Errorf("created at %v/%d, want commit %v/%d", l.CreatedAt, l.CreatedSeq, created.Time, created.Seq)
	}
	if l.LastSlot != 0 || l.Stopped() {
		t.Errorf("fresh loop has LastSlot=%d stopped=%v", l.LastSlot, l.Stopped())
	}
	if got := l.SlotTime(3); !got.Equal(created.Time.Add(15 * time.Minute)) {
		t.Errorf("SlotTime(3) = %v, want creation + 15m", got)
	}

	// Slots may be skipped: slot 2 straight after creation is a normal delivery, nothing is journaled for slot 1.
	mustTickLoop(t, store, "loop-a", 2)
	if got := store.Snapshot().Assistants.Loops["loop-a"].LastSlot; got != 2 {
		t.Errorf("LastSlot = %d, want 2", got)
	}

	stopped, err := applyLoopOp(store, "stop-a", Actor{Kind: ActorKindUser}, loopEffect(t, kindLoopStopped, "loop-a", nil))
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	l = store.Snapshot().Assistants.Loops["loop-a"]
	if !l.Stopped() || l.StoppedBy != (Actor{Kind: ActorKindUser}) || !l.StoppedAt.Equal(stopped.Time) || l.StoppedSeq != stopped.Seq {
		t.Errorf("stop not recorded: %+v", *l)
	}
	if l.LastSlot != 2 {
		t.Errorf("stop changed LastSlot to %d", l.LastSlot)
	}
}

func TestLoopReplayMatchesLiveProjection(t *testing.T) {
	dir := t.TempDir()
	store := openLoopStore(t, dir)
	mustCreateLoop(t, store, "loop-a")
	mustCreateLoop(t, store, "loop-b")
	mustTickLoop(t, store, "loop-a", 1)
	mustTickLoop(t, store, "loop-a", 3)
	if _, err := applyLoopOp(store, "stop-b", Actor{Kind: "agent", ID: "agent-1"}, loopEffect(t, kindLoopStopped, "loop-b", nil)); err != nil {
		t.Fatalf("stop: %v", err)
	}
	live := store.Snapshot().Assistants

	replayed, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	if !reflect.DeepEqual(live.Loops, replayed.Assistants.Loops) {
		t.Errorf("loops differ\nlive:     %+v\nreplayed: %+v", live.Loops, replayed.Assistants.Loops)
	}
	if !reflect.DeepEqual(live.LoopOrder, replayed.Assistants.LoopOrder) {
		t.Errorf("order differs: live %v, replayed %v", live.LoopOrder, replayed.Assistants.LoopOrder)
	}
	if got := replayed.Assistants.Loops["loop-b"].ShortID; got != "L2" {
		t.Errorf("replayed short id of loop-b = %q, want L2", got)
	}
}

func TestLoopSnapshotIsIndependentOfLaterCommits(t *testing.T) {
	store := openLoopStore(t, t.TempDir())
	mustCreateLoop(t, store, "loop-a")
	before := store.Snapshot().Assistants

	mustTickLoop(t, store, "loop-a", 1)
	mustCreateLoop(t, store, "loop-b")
	if _, err := applyLoopOp(store, "stop-a", Actor{Kind: ActorKindUser}, loopEffect(t, kindLoopStopped, "loop-a", nil)); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if l := before.Loops["loop-a"]; l.LastSlot != 0 || l.Stopped() {
		t.Errorf("earlier snapshot changed after later commits: %+v", *l)
	}
	if len(before.Loops) != 1 || len(before.LoopOrder) != 1 {
		t.Errorf("earlier snapshot gained loops: %d / %v", len(before.Loops), before.LoopOrder)
	}
	// Mutating a returned snapshot must not reach the store either.
	snap := store.Snapshot().Assistants
	delete(snap.Loops, "loop-a")
	snap.LoopOrder[0] = "tampered"
	again := store.Snapshot().Assistants
	if again.Loops["loop-a"] == nil || again.LoopOrder[0] != "loop-a" {
		t.Errorf("snapshot mutation leaked into the store: %v %v", again.Loops, again.LoopOrder)
	}
}

func TestLoopConcurrentCreatesGetDistinctShortIDs(t *testing.T) {
	store := openLoopStore(t, t.TempDir())
	const n = 8
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			key := fmt.Sprintf("loop-%d", i)
			if _, err := applyLoopOp(store, "create-"+key, Actor{Kind: ActorKindUser}, loopEffect(t, kindLoopCreated, key, loopCreatePayload(key))); err != nil {
				t.Errorf("create %s: %v", key, err)
			}
		})
	}
	wg.Wait()

	as := store.Snapshot().Assistants
	seen := map[string]bool{}
	for i, key := range as.LoopOrder {
		short := as.Loops[key].ShortID
		if want := fmt.Sprintf("L%d", i+1); short != want {
			t.Errorf("loop #%d (%s) has short id %s, want %s", i+1, key, short, want)
		}
		seen[short] = true
	}
	if len(as.LoopOrder) != n || len(seen) != n {
		t.Errorf("got %d loops / %d distinct short ids, want %d", len(as.LoopOrder), len(seen), n)
	}
}

func TestLoopCreateRetryAndConflict(t *testing.T) {
	dir := t.TempDir()
	store := openLoopStore(t, dir)
	create := loopEffect(t, kindLoopCreated, "loop-a", loopCreatePayload("loop-a"))

	first, err := applyLoopOp(store, "op-1", Actor{Kind: ActorKindUser}, create)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	size := journalSize(t, dir)

	retry, err := applyLoopOp(store, "op-1", Actor{Kind: ActorKindUser}, create)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retry.Seq != first.Seq || !retry.Time.Equal(first.Time) {
		t.Errorf("retry = seq %d at %v, want the original %d at %v", retry.Seq, retry.Time, first.Seq, first.Time)
	}
	if journalSize(t, dir) != size || len(store.Snapshot().Assistants.LoopOrder) != 1 {
		t.Error("retry wrote a second schedule")
	}

	other := loopCreatePayload("loop-a")
	other.Prompt = "something else"
	_, err = applyLoopOp(store, "op-1", Actor{Kind: ActorKindUser}, loopEffect(t, kindLoopCreated, "loop-a", other))
	if !errors.Is(err, ErrOperationIDConflict) {
		t.Errorf("same op id, different content: err = %v, want ErrOperationIDConflict", err)
	}
}

func TestLoopRejectsRepeatedMutation(t *testing.T) {
	dir := t.TempDir()
	store := openLoopStore(t, dir)
	mustCreateLoop(t, store, "loop-a")
	mustTickLoop(t, store, "loop-a", 2)
	user := Actor{Kind: ActorKindUser}

	// A second create under a new operation id for the same key is not a second schedule.
	_, err := applyLoopOp(store, "dup-create", user, loopEffect(t, kindLoopCreated, "loop-a", loopCreatePayload("loop-a")))
	requireRejected(t, err, assistants.CodeInvalidStatus)

	// A slot already delivered, or older than the last one, never produces a second tick.
	for _, slot := range []uint64{2, 1} {
		_, err = applyLoopOp(store, fmt.Sprintf("dup-tick-%d", slot), user, loopEffect(t, kindLoopTicked, "loop-a", LoopTickedPayload{Slot: slot}))
		requireRejected(t, err, assistants.CodeInvalidStatus)
	}

	if _, err := applyLoopOp(store, "stop-1", user, loopEffect(t, kindLoopStopped, "loop-a", nil)); err != nil {
		t.Fatalf("stop: %v", err)
	}
	_, err = applyLoopOp(store, "stop-2", user, loopEffect(t, kindLoopStopped, "loop-a", nil))
	requireRejected(t, err, assistants.CodeInvalidStatus)
	_, err = applyLoopOp(store, "tick-after-stop", user, loopEffect(t, kindLoopTicked, "loop-a", LoopTickedPayload{Slot: 3}))
	requireRejected(t, err, assistants.CodeInvalidStatus)

	l := store.Snapshot().Assistants.Loops["loop-a"]
	if l.LastSlot != 2 || len(store.Snapshot().Assistants.LoopOrder) != 1 {
		t.Errorf("rejected mutations changed state: %+v", *l)
	}
}

func TestLoopMalformedRecordsAreRejectedWithoutAppend(t *testing.T) {
	dir := t.TempDir()
	store := openLoopStore(t, dir)
	mustCreateLoop(t, store, "loop-a")
	size := journalSize(t, dir)
	user := Actor{Kind: ActorKindUser}

	with := func(mutate func(*LoopCreatedPayload)) LoopCreatedPayload {
		p := loopCreatePayload("loop-new")
		mutate(&p)
		return p
	}
	cases := map[string]struct {
		effect Effect
		code   assistants.ErrorCode
	}{
		"empty key":     {loopEffect(t, kindLoopCreated, "", loopCreatePayload("x")), assistants.CodeInvalidRequest},
		"no creator":    {loopEffect(t, kindLoopCreated, "loop-new", with(func(p *LoopCreatedPayload) { p.CreatorID = "" })), assistants.CodeInvalidRequest},
		"no target":     {loopEffect(t, kindLoopCreated, "loop-new", with(func(p *LoopCreatedPayload) { p.TargetID = "" })), assistants.CodeInvalidRequest},
		"zero interval": {loopEffect(t, kindLoopCreated, "loop-new", with(func(p *LoopCreatedPayload) { p.Interval = 0 })), assistants.CodeInvalidRequest},
		"negative":      {loopEffect(t, kindLoopCreated, "loop-new", with(func(p *LoopCreatedPayload) { p.Interval = -time.Second })), assistants.CodeInvalidRequest},
		"empty prompt":  {loopEffect(t, kindLoopCreated, "loop-new", with(func(p *LoopCreatedPayload) { p.Prompt = "" })), assistants.CodeInvalidRequest},
		"no thread":     {loopEffect(t, kindLoopCreated, "loop-new", with(func(p *LoopCreatedPayload) { p.ThreadID = "" })), assistants.CodeInvalidRequest},
		"zero slot":     {loopEffect(t, kindLoopTicked, "loop-a", LoopTickedPayload{Slot: 0}), assistants.CodeInvalidRequest},
		"tick unknown":  {loopEffect(t, kindLoopTicked, "nope", LoopTickedPayload{Slot: 1}), assistants.CodeNotFound},
		"stop unknown":  {loopEffect(t, kindLoopStopped, "nope", nil), assistants.CodeNotFound},
		"no loop key":   {Effect{Kind: kindLoopStopped}, assistants.CodeInvalidRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := applyLoopOp(store, "bad-"+strings.ReplaceAll(name, " ", "-"), user, tc.effect)
			requireRejected(t, err, tc.code)
		})
	}
	if journalSize(t, dir) != size {
		t.Error("a rejected record reached the journal")
	}

	// A batch whose later effect is malformed leaves no trace of the earlier, valid one.
	_, err := applyLoopOp(store, "batch", user,
		loopEffect(t, kindLoopCreated, "loop-b", loopCreatePayload("loop-b")),
		loopEffect(t, kindLoopTicked, "loop-b", LoopTickedPayload{Slot: 0}),
	)
	requireRejected(t, err, assistants.CodeInvalidRequest)
	as := store.Snapshot().Assistants
	if _, ok := as.Loops["loop-b"]; ok || len(as.LoopOrder) != 1 || journalSize(t, dir) != size {
		t.Errorf("partial batch applied: order %v, journal grew %v", as.LoopOrder, journalSize(t, dir) != size)
	}
}

func TestLoopCreateAndTickInOneOperationSeesTheNewLoop(t *testing.T) {
	store := openLoopStore(t, t.TempDir())
	_, err := applyLoopOp(store, "both", Actor{Kind: ActorKindUser},
		loopEffect(t, kindLoopCreated, "loop-a", loopCreatePayload("loop-a")),
		loopEffect(t, kindLoopTicked, "loop-a", LoopTickedPayload{Slot: 1}),
	)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if got := store.Snapshot().Assistants.Loops["loop-a"].LastSlot; got != 1 {
		t.Errorf("LastSlot = %d, want 1", got)
	}
}

func TestLoopLookupHelpers(t *testing.T) {
	store := openLoopStore(t, t.TempDir())
	mustCreateLoop(t, store, "loop-b")
	mustCreateLoop(t, store, "loop-a")
	as := store.Snapshot().Assistants

	if l, ok := as.LoopByShortID("L2"); !ok || l.Key != "loop-a" {
		t.Errorf("LoopByShortID(L2) = %+v, %v; want loop-a", l, ok)
	}
	if _, ok := as.LoopByShortID("L3"); ok {
		t.Error("LoopByShortID(L3) found a loop that does not exist")
	}
	var keys []string
	for _, l := range as.OrderedLoops() {
		keys = append(keys, l.Key)
	}
	if !reflect.DeepEqual(keys, []string{"loop-b", "loop-a"}) {
		t.Errorf("OrderedLoops = %v, want creation order [loop-b loop-a]", keys)
	}
}
