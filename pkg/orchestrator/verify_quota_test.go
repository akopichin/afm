package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

// codexVerifyStep — голый codex-as-claude: провайдер codex определяет общий resolver, а не имя в тесте.
var codexVerifyStep = flow.VerifyStep{Kind: flow.VerifyAgent, Command: "codex-as-claude"}

func verifyQuotaStages(spec flow.VerifySpec) []flow.Stage {
	return []flow.Stage{{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentImplementation}, Verify: spec}}
}

func codexLimit(resetAt time.Time) *executor.UsageLimitError {
	return &executor.UsageLimitError{
		Provider: "codex", ResetAt: resetAt, Message: "You've hit your usage limit. Try again at 11:50 PM.",
		Source: executor.UsageLimitSourceText,
	}
}

// verifyAgentScript — программируемый исход каждого вызова agent-шага AI-verify.
type verifyAgentScript struct {
	mu      sync.Mutex
	calls   int
	prompts []string
	script  func(call int) (verify.RunOutcome, error)
}

func (v *verifyAgentScript) run(_ context.Context, _ flow.Stage, _, prompt, _, _ string) (verify.RunOutcome, error) {
	v.mu.Lock()
	v.calls++
	n := v.calls
	v.prompts = append(v.prompts, prompt)
	v.mu.Unlock()
	return v.script(n)
}

func (v *verifyAgentScript) callCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.calls
}

func (v *verifyAgentScript) promptOf(call int) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.prompts[call-1]
}

func (v *verifyAgentScript) install(o *orchestrator.Orchestrator) {
	orchestrator.SetVerifyAgentRunnerForTest(o, v.run)
}

func verdictOutcome(verdict verify.Verdict, summary string) verify.RunOutcome {
	return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
		SchemaVersion: verify.SupportedSchemaVersion, Verdict: verdict, Summary: summary,
		Findings: []verify.Finding{{Blocking: verdict == verify.VerdictNeedsChanges, Title: "t", Requirement: "r", Evidence: "e", MinimalFix: "f"}},
	}}
}

func passOutcome(summary string) verify.RunOutcome {
	return verdictOutcome(verify.VerdictPass, summary)
}

func noticeTypes(t *testing.T, runDir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Type != "" {
			out = append(out, rec.Type)
		}
	}
	return out
}

// lastTransitionReason — причина последнего перехода стадии из журнала.
func lastTransitionReason(t *testing.T, store *state.Store, stageID string) string {
	t.Helper()
	history, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	reason := ""
	for _, tr := range history {
		if tr.StageID == stageID {
			reason = tr.Reason
		}
	}
	return reason
}

func hasNotice(types []string, want bus.EventType) bool {
	for _, tp := range types {
		if tp == string(want) {
			return true
		}
	}
	return false
}

// twoStepSpec — два agent-шага; первый всегда pass, на втором упираемся в лимит.
func twoStepSpec() flow.VerifySpec {
	return flow.VerifySpec{Steps: []flow.VerifyStep{codexVerifyStep, codexVerifyStep}}
}

// limitAtSecondStep: шаг 1 — pass; первый вызов шага 2 — лимит до reset; дальше всё pass.
func limitAtSecondStep(clk *clock.FakeClock, reset time.Duration) *verifyAgentScript {
	return &verifyAgentScript{script: func(call int) (verify.RunOutcome, error) {
		if call == 2 {
			return verify.RunOutcome{}, codexLimit(clk.Now().Add(reset))
		}
		return passOutcome("step ok"), nil
	}}
}

func authorOnce() *quotaRunner { return &quotaRunner{script: func(int, string) error { return nil }} }

func TestVerifyQuota_LimitParksVerifyAndAuthorIsNotCalledAgain(t *testing.T) {
	runDir := t.TempDir()
	clk := clock.NewFake(quotaStart)
	author := authorOnce()
	agent := limitAtSecondStep(clk, 2*time.Minute)
	rig := startQuotaRig(t, runDir, verifyQuotaStages(twoStepSpec()), quotaConfig(), author, clk, true, agent.install)

	q := rig.waitParked("s1")
	if q.Target != state.QuotaTargetVerify || q.VerifyStep != 2 || q.Provider != "codex" || q.Waits != 1 || q.VerifyID == "" {
		t.Fatalf("park = %+v, want target=verify step=2 provider=codex waits=1 and a verification id", *q)
	}
	events := journalEvents(t, runDir)
	if n := countEvent(events, bus.EvVerifyFail) + countEvent(events, bus.EvFail); n != 0 {
		t.Errorf("a usage limit during verify failed the stage: events = %v", events)
	}
	if hasNotice(noticeTypes(t, runDir), bus.EventVerifyRetry) {
		t.Error("a usage limit during verify must not emit verify_retry")
	}
	if author.callCount() != 1 {
		t.Fatalf("author calls = %d, want 1", author.callCount())
	}

	clk.Advance(5 * time.Minute) // reset + resume_margin ± джиттер
	rig.waitStatus("s1", state.StatusDone)
	if got := author.callCount(); got != 1 {
		t.Errorf("author calls after the wake = %d, want 0 more (1 total)", got)
	}
	if got := agent.callCount(); got != 3 {
		t.Errorf("verify agent calls = %d, want 3: step 1, refused step 2, repeated step 2 only", got)
	}
	if p := agent.promptOf(3); !strings.Contains(p, "Step 1 (agent codex-as-claude): pass") {
		t.Errorf("repeated step must see the restored accepted step 1; prompt:\n%s", p)
	}
}

func TestVerifyQuota_ParkKeepsVerifyCountersAndNeverSpendsTheBudget(t *testing.T) {
	runDir := t.TempDir()
	clk := clock.NewFake(quotaStart)
	author := authorOnce()
	agent := &verifyAgentScript{script: func(call int) (verify.RunOutcome, error) {
		switch call {
		case 1, 2:
			return verdictOutcome(verify.VerdictNeedsChanges, "not yet"), nil
		case 3:
			return verify.RunOutcome{}, codexLimit(clk.Now().Add(2 * time.Minute))
		default:
			return passOutcome("ok"), nil
		}
	}}
	five := 5
	spec := flow.VerifySpec{Steps: []flow.VerifyStep{codexVerifyStep}, MaxFailures: &five, ReviewPatience: &five}
	rig := startQuotaRig(t, runDir, verifyQuotaStages(spec), quotaConfig(), author, clk, true, agent.install, func(o *orchestrator.Orchestrator) {
		orchestrator.SetFingerprintFnForTest(o, func(string) (string, bool) { return "same-tree", true })
	})

	q := rig.waitParked("s1")
	if q.Target != state.QuotaTargetVerify || q.VerifyStep != 1 || q.VerifyFailures != 2 || q.VerifyStalemates != 1 {
		t.Fatalf("park = %+v, want verify step 1 with failures=2 stalemates=1", *q)
	}
	if author.callCount() != 3 {
		t.Fatalf("author calls = %d, want 3 (initial + two corrections)", author.callCount())
	}

	clk.Advance(5 * time.Minute)
	rig.waitStatus("s1", state.StatusDone)
	if got := author.callCount(); got != 3 {
		t.Errorf("author calls = %d, want 3: the wake must not call the author", got)
	}
}

func TestVerifyQuota_ResumeKeepsCountersButResetsFingerprint(t *testing.T) {
	runDir := t.TempDir()
	clk := clock.NewFake(quotaStart)
	author := authorOnce()
	agent := &verifyAgentScript{script: func(call int) (verify.RunOutcome, error) {
		switch call {
		case 3, 4, 6:
			return verify.RunOutcome{}, codexLimit(clk.Now().Add(2 * time.Minute))
		default:
			return verdictOutcome(verify.VerdictNeedsChanges, "not yet"), nil
		}
	}}
	maxFailures, patience := 3, 2
	spec := flow.VerifySpec{Steps: []flow.VerifyStep{codexVerifyStep}, MaxFailures: &maxFailures, ReviewPatience: &patience}
	rig := startQuotaRig(t, runDir, verifyQuotaStages(spec), quotaConfig(), author, clk, true, agent.install, func(o *orchestrator.Orchestrator) {
		orchestrator.SetFingerprintFnForTest(o, func(string) (string, bool) { return "same-tree", true })
	})

	q := rig.waitParked("s1")
	if q.VerifyFailures != 2 || q.VerifyStalemates != 1 {
		t.Fatalf("first park = %+v, want failures=2 stalemates=1", *q)
	}
	clk.Advance(5 * time.Minute)
	q = rig.waitParkedAgain("s1", q.Until)
	clk.BlockUntil(2)
	if q.VerifyFailures != 2 || q.VerifyStalemates != 1 || author.callCount() != 3 {
		t.Fatalf("repeated quota park = %+v, author calls = %d, want unchanged counters and 3 calls", *q, author.callCount())
	}

	// The first rejection after wake establishes a baseline even when the tree has not changed.
	clk.Advance(5 * time.Minute)
	q = rig.waitParkedAgain("s1", q.Until)
	clk.BlockUntil(2)
	if q.VerifyFailures != 3 || q.VerifyStalemates != 0 || author.callCount() != 4 {
		t.Fatalf("park after correction = %+v, author calls = %d, want failures=3 stalemates=0 and 4 calls", *q, author.callCount())
	}

	clk.Advance(5 * time.Minute)
	rig.waitStatus("s1", state.StatusFailed)
	if author.callCount() != 4 || agent.callCount() != 7 {
		t.Errorf("author/verify calls = %d/%d, want 4/7 (resumed verify budget exhausted)", author.callCount(), agent.callCount())
	}
	if got := countEvent(journalEvents(t, runDir), bus.EvVerifyFail); got != 1 {
		t.Errorf("verify-fail transitions = %d, want 1", got)
	}
}

func TestVerifyQuota_ReopenWaitsRemainderAndRepeatsOnlyTheStep(t *testing.T) {
	runDir := t.TempDir()
	clk := clock.NewFake(quotaStart)
	author := authorOnce()
	agent := limitAtSecondStep(clk, 2*time.Minute)
	rig := startQuotaRig(t, runDir, verifyQuotaStages(twoStepSpec()), quotaConfig(), author, clk, true, agent.install)
	parked := rig.waitParked("s1")
	until := parked.Until

	rig.stop()
	events := journalEvents(t, runDir)
	if countEvent(events, bus.EvFail)+countEvent(events, bus.EvVerifyFail) != 0 {
		t.Fatalf("cancelling the run must not fail a verify wait: events = %v", events)
	}
	rs, err := state.LoadRunState(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Stages["s1"]; got.Status != state.StatusAwaitingQuota || got.Quota == nil ||
		got.Quota.Target != state.QuotaTargetVerify || !got.Quota.Until.Equal(until) {
		t.Fatalf("journal after cancel = %+v quota=%+v", got, got.Quota)
	}

	clk2 := clock.NewFake(quotaStart.Add(time.Minute))
	rig2 := startQuotaRig(t, runDir, verifyQuotaStages(twoStepSpec()), quotaConfig(), author, clk2, false, agent.install)
	rig2.waitStatus("s1", state.StatusAwaitingQuota)
	clk2.BlockUntil(2)
	clk2.Advance(time.Minute + 30*time.Second) // до until
	clk2.BlockUntil(2)
	if agent.callCount() != 2 {
		t.Fatalf("woke before the remainder elapsed: %d verify calls", agent.callCount())
	}
	clk2.Advance(time.Minute)
	rig2.waitStatus("s1", state.StatusDone)
	if author.callCount() != 1 || agent.callCount() != 3 {
		t.Errorf("author calls = %d (want 1), verify calls = %d (want 3)", author.callCount(), agent.callCount())
	}
}

func TestVerifyQuota_ExpiredUntilAfterRestartRunsExactlyOneVerifyStep(t *testing.T) {
	runDir := t.TempDir()
	clk := clock.NewFake(quotaStart)
	author := authorOnce()
	agent := limitAtSecondStep(clk, 2*time.Minute)
	rig := startQuotaRig(t, runDir, verifyQuotaStages(twoStepSpec()), quotaConfig(), author, clk, true, agent.install)
	rig.waitParked("s1")
	rig.stop()

	rig2 := startQuotaRig(t, runDir, verifyQuotaStages(twoStepSpec()), quotaConfig(), author, clock.NewFake(quotaStart.Add(time.Hour)), false, agent.install)
	rig2.waitStatus("s1", state.StatusDone)
	orchestrator.WaitAgentsForTest(rig2.orch)
	if author.callCount() != 1 || agent.callCount() != 3 {
		t.Errorf("author calls = %d (want 1), verify calls = %d (want 3)", author.callCount(), agent.callCount())
	}
	if n := countEvent(journalEvents(t, runDir), bus.EvQuotaReset); n != 1 {
		t.Errorf("quota_reset count = %d, want 1", n)
	}
}

func TestVerifyQuota_PauseAndManualProbeKeepTheVerifyTarget(t *testing.T) {
	runDir := t.TempDir()
	clk := clock.NewFake(quotaStart)
	author := authorOnce()
	agent := &verifyAgentScript{script: func(call int) (verify.RunOutcome, error) {
		if call == 2 || call == 3 { // шаг 2 отказан дважды: сначала сам, потом на ручной пробе
			return verify.RunOutcome{}, codexLimit(clk.Now().Add(time.Hour))
		}
		return passOutcome("ok"), nil
	}}
	rig := startQuotaRig(t, runDir, verifyQuotaStages(twoStepSpec()), quotaConfig(), author, clk, true, agent.install)
	rig.waitParked("s1")
	ctx := context.Background()

	if err := rig.orch.Pause(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	st := rig.stage("s1")
	if st.Status != state.StatusPaused || st.PausedFrom != state.StatusAwaitingQuota || st.Quota == nil ||
		st.Quota.Target != state.QuotaTargetVerify || st.Quota.VerifyStep != 2 {
		t.Fatalf("after Pause: %+v quota=%+v, want the verify target kept", st, st.Quota)
	}

	if err := rig.orch.Continue(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "manual probe of verify step 2", func() bool { return agent.callCount() == 3 })
	q := rig.waitParked("s1")
	if q.Target != state.QuotaTargetVerify || q.VerifyStep != 2 || q.Waits != 1 || q.ManualProbe {
		t.Errorf("quota after the refused probe = %+v, want verify step 2, new series (waits=1), no probe intent", *q)
	}

	clk.BlockUntil(3) // тикер dialog-поллера, неотработавший таймер первого парка и таймер нового
	clk.Advance(2 * time.Hour)
	rig.waitStatus("s1", state.StatusDone)
	if author.callCount() != 1 || agent.callCount() != 4 {
		t.Errorf("author calls = %d (want 1), verify calls = %d (want 4)", author.callCount(), agent.callCount())
	}
}

func TestVerifyQuota_ReviewPauseBlocksWakeUntilResumed(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	author := authorOnce()
	agent := limitAtSecondStep(clk, 2*time.Minute)
	rig := startQuotaRig(t, t.TempDir(), verifyQuotaStages(twoStepSpec()), quotaConfig(), author, clk, true, agent.install)
	rig.waitParked("s1")
	ctx := context.Background()

	if _, err := rig.orch.PauseFlow(ctx); err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Minute)
	neverHappens(t, "verify wake during review pause", func() bool {
		return agent.callCount() > 2 || rig.stage("s1").Status != state.StatusAwaitingQuota
	})

	if err := rig.orch.CancelNotesAndResume(ctx); err != nil {
		t.Fatal(err)
	}
	rig.waitStatus("s1", state.StatusDone)
	if author.callCount() != 1 || agent.callCount() != 3 {
		t.Errorf("author calls = %d (want 1), verify calls = %d (want 3)", author.callCount(), agent.callCount())
	}
}

func TestVerifyQuota_CorruptManifestFailsClosedAsVerifyStorageError(t *testing.T) {
	runDir := t.TempDir()
	clk := clock.NewFake(quotaStart)
	author := authorOnce()
	agent := limitAtSecondStep(clk, 2*time.Minute)
	rig := startQuotaRig(t, runDir, verifyQuotaStages(twoStepSpec()), quotaConfig(), author, clk, true, agent.install)
	q := rig.waitParked("s1")
	rig.stop()

	manifest := filepath.Join(runDir, "s1", "verify", q.VerifyID, "manifest.json")
	if err := os.WriteFile(manifest, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	rig2 := startQuotaRig(t, runDir, verifyQuotaStages(twoStepSpec()), quotaConfig(), author, clock.NewFake(quotaStart.Add(time.Hour)), false, agent.install)
	rig2.waitStatus("s1", state.StatusFailed)
	if reason := lastTransitionReason(t, rig2.store, "s1"); !strings.Contains(reason, "verify storage failure") {
		t.Errorf("fail reason = %q, want the existing verify storage failure semantics", reason)
	}
	if n := countEvent(journalEvents(t, runDir), bus.EvVerifyFail); n != 1 {
		t.Errorf("verify_fail count = %d, want 1 (fail-closed)", n)
	}
	if author.callCount() != 1 || agent.callCount() != 2 {
		t.Errorf("author calls = %d (want 1), verify calls = %d (want 2): a corrupt manifest must run nothing", author.callCount(), agent.callCount())
	}
}

func TestVerifyQuota_TransientExitStaysExecFailureWithoutPark(t *testing.T) {
	runDir := t.TempDir()
	clk := clock.NewFake(quotaStart)
	author := authorOnce()
	agent := &verifyAgentScript{script: func(call int) (verify.RunOutcome, error) {
		if call == 1 {
			return verify.RunOutcome{ProcessOK: false}, nil // процесс стартовал и упал с ненулевым кодом
		}
		return passOutcome("ok"), nil
	}}
	rig := startQuotaRig(t, runDir, verifyQuotaStages(flow.VerifySpec{Steps: []flow.VerifyStep{codexVerifyStep}}), quotaConfig(), author, clk, true, agent.install)

	waitFor(t, "verify_retry notice", func() bool { return hasNotice(noticeTypes(t, runDir), bus.EventVerifyRetry) })
	clk.BlockUntil(2) // backoff-таймер verify и тикер dialog-поллера
	clk.Advance(time.Minute)
	rig.waitStatus("s1", state.StatusDone)
	if n := countEvent(journalEvents(t, runDir), bus.EvAwaitQuota); n != 0 {
		t.Errorf("a transient non-zero exit must not park: await_quota events = %d", n)
	}
	if agent.callCount() != 2 {
		t.Errorf("verify calls = %d, want 2 (failure + retry)", agent.callCount())
	}
}

// Hold провайдера на admission verify-агента паркует стадию тем же путём: source provider_hold, until ровно конец
// чужого парка без своего джиттера, счётчик отказов не растёт.
func TestVerifyQuota_ProviderHoldAtVerifyAdmissionParksTheStage(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	aParked := make(chan struct{})
	author := newStageRunner(func(ctx context.Context, stageID string, call int) error {
		switch {
		case stageID == "a" && call == 1:
			return codexLimit(clk.Now().Add(2 * time.Hour))
		case stageID == "b" && call == 1:
			select { // verify стадии b стартует только после парка a
			case <-aParked:
			case <-ctx.Done():
			}
		default:
		}
		return nil
	})
	agent := &verifyAgentScript{script: func(int) (verify.RunOutcome, error) { return passOutcome("ok"), nil }}
	stages := []flow.Stage{
		{ID: "a", Name: "A", Agents: []flow.AgentType{flow.AgentImplementation}},
		{ID: "b", Name: "B", Agents: []flow.AgentType{flow.AgentImplementation}, Verify: flow.VerifySpec{Steps: []flow.VerifyStep{codexVerifyStep}}},
	}
	rig := startQuotaRig(t, t.TempDir(), stages, quotaConfig(), author, clk, true, agent.install)

	rig.waitStatus("a", state.StatusAwaitingQuota)
	close(aParked)
	rig.waitStatus("b", state.StatusAwaitingQuota)
	a, b := rig.stage("a"), rig.stage("b")

	if b.Quota.Source != state.QuotaSourceProviderHold || b.Quota.Target != state.QuotaTargetVerify || b.Quota.VerifyStep != 1 {
		t.Errorf("b park = %+v, want a provider_hold park of verify step 1", *b.Quota)
	}
	if !b.Quota.Until.Equal(a.Quota.Until) {
		t.Errorf("hold until = %s, want exactly a's %s (no own jitter)", b.Quota.Until, a.Quota.Until)
	}
	if b.Quota.Waits != 0 || b.Quota.PauseReason != "" {
		t.Errorf("b waits = %d reason = %q, want waits=0 (a hold is not a refusal) and no pause", b.Quota.Waits, b.Quota.PauseReason)
	}
	if agent.callCount() != 0 {
		t.Errorf("verify agent ran under the hold: %d calls", agent.callCount())
	}

	rig.clk.BlockUntil(3)
	clk.Advance(2*time.Hour + 2*time.Minute)
	rig.waitStatus("a", state.StatusDone)
	rig.waitStatus("b", state.StatusDone)
	if got := len(author.promptsOf("b")); got != 1 {
		t.Errorf("author b calls = %d, want 1: the hold wake runs only the verify step", got)
	}
	if agent.callCount() != 1 {
		t.Errorf("verify calls = %d, want 1", agent.callCount())
	}
}

// Устаревший ход автора (старая эпоха) и таймер старого парка не двигают новый verify-парк.
func TestVerifyQuota_StaleParkAndStaleTimerChangeNothing(t *testing.T) {
	runDir := t.TempDir()
	stages := verifyQuotaStages(twoStepSpec())
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	author := authorOnce()
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: quotaConfig(),
		Prompts: orchestrator.DefaultPrompts(), Runner: author, Clock: clock.NewFake(quotaStart),
	})
	fsm := bus.NewFSM(store)
	must := func(ev bus.FSMEvent, ctx bus.GuardCtx) {
		t.Helper()
		if _, _, _, ok, err := fsm.Apply("s1", ev, ctx, ""); err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", ev, ok, err)
		}
	}
	must(bus.EvReady, bus.GuardCtx{})
	must(bus.EvStartRun, bus.GuardCtx{})
	oldEpoch := store.Snapshot().Stages["s1"].Epoch
	must(bus.EvPause, bus.GuardCtx{})
	must(bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusRunning})

	limit := codexLimit(quotaStart.Add(time.Hour))
	orchestrator.ParkVerifyForQuotaForTest(orch, stages[0], oldEpoch, limit, "v-1", 2)
	if st := store.Snapshot().Stages["s1"]; st.Status != state.StatusRunning || st.Quota != nil {
		t.Fatalf("stale verify park changed the stage: %+v quota=%+v", st, st.Quota)
	}

	curEpoch := store.Snapshot().Stages["s1"].Epoch
	orchestrator.ParkVerifyForQuotaForTest(orch, stages[0], curEpoch, limit, "v-1", 2)
	first := store.Snapshot().Stages["s1"]
	if first.Status != state.StatusAwaitingQuota || first.Quota.Target != state.QuotaTargetVerify || first.Quota.VerifyStep != 2 || first.Quota.VerifyID != "v-1" {
		t.Fatalf("park = %+v quota=%+v", first, first.Quota)
	}
	must(bus.EvPause, bus.GuardCtx{})
	must(bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusAwaitingQuota, Quota: &state.QuotaWait{
		Provider: "codex", Until: quotaStart.Add(time.Hour), Target: state.QuotaTargetVerify, VerifyStep: 2, VerifyID: "v-1", ManualProbe: true,
	}})
	second := store.Snapshot().Stages["s1"]
	orchestrator.WakeFromQuotaForTest(orch, "s1", first.Seq) // таймер первого парка
	if st := store.Snapshot().Stages["s1"]; st.Status != state.StatusAwaitingQuota || st.Seq != second.Seq {
		t.Fatalf("old timer moved the newer park: %+v", st)
	}
	if author.callCount() != 0 {
		t.Errorf("old timer spawned an agent: %d calls", author.callCount())
	}
}

// reviseText — текст фидбека Revise в тестах владения verify-приказом.
const reviseText = "please change X"

// wokenVerifyRig — ран, где verify-шаг 2 упёрся в лимит, стадия проснулась и повторяет шаг 2 (вызов агента 3):
// он стоит, пока тест не откроет gate. onStep3 — что делает вызов 3, пока стоит.
type wokenVerifyRig struct {
	*quotaRig
	agent   *verifyAgentScript
	entered chan struct{} // закрывается, когда повторённый шаг 2 начался
	gate    chan struct{} // открывает повторённый шаг 2
}

func startWokenVerifyRig(t *testing.T, author executor.Runner, finalStepOutcome func() verify.RunOutcome) *wokenVerifyRig {
	t.Helper()
	clk := clock.NewFake(quotaStart)
	w := &wokenVerifyRig{entered: make(chan struct{}), gate: make(chan struct{})}
	w.agent = &verifyAgentScript{script: func(call int) (verify.RunOutcome, error) {
		switch call {
		case 2:
			return verify.RunOutcome{}, codexLimit(clk.Now().Add(2 * time.Minute))
		case 3:
			close(w.entered)
			<-w.gate
			return finalStepOutcome(), nil
		default:
			return passOutcome("step ok"), nil
		}
	}}
	w.quotaRig = startQuotaRig(t, t.TempDir(), verifyQuotaStages(twoStepSpec()), quotaConfig(), author, clk, true, w.agent.install)
	w.waitParked("s1")
	clk.Advance(5 * time.Minute)
	select {
	case <-w.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the woken verify step did not start")
	}
	return w
}

func (w *wokenVerifyRig) revise() {
	w.t.Helper()
	if applied, _, err := w.orch.Revise(context.Background(), "s1", reviseText); err != nil || !applied {
		w.t.Fatalf("Revise = %v, %v; want applied", applied, err)
	}
}

// verifyPassDirs — сколько проходов verify оставило манифест: новый проход после фидбека — новый VerifyID.
func verifyPassDirs(t *testing.T, runDir string) int {
	t.Helper()
	manifests, err := filepath.Glob(filepath.Join(runDir, "s1", "verify", "*", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	return len(manifests)
}

// Revise во время повторённого шага verify после пробуждения: приказ пробуждения принадлежит одному циклу, поэтому
// feedback-ход вызывает автора с текстом фидбека, а verify начинает НОВЫЙ проход — стадия не доходит до done без
// feedback-хода.
func TestVerifyQuota_ReviseDuringWokenVerifyStepRunsFeedbackAuthorAndFreshPass(t *testing.T) {
	author := authorOnce()
	w := startWokenVerifyRig(t, author, func() verify.RunOutcome { return passOutcome("step ok") })

	w.revise()
	close(w.gate)
	w.waitStatus("s1", state.StatusDone)
	orchestrator.WaitAgentsForTest(w.orch)

	if got := author.callCount(); got != 2 {
		t.Fatalf("author calls = %d, want 2: the initial turn and exactly one feedback run", got)
	}
	if p := author.promptOf(2); !strings.Contains(p, reviseText) {
		t.Errorf("feedback author prompt lacks the user's feedback %q:\n%s", reviseText, p)
	}
	if got := w.agent.callCount(); got != 5 {
		t.Errorf("verify calls = %d, want 5: step 1, refused step 2, repeated step 2, then a fresh pass of both steps", got)
	}
	if got := verifyPassDirs(t, w.runDir); got != 2 {
		t.Errorf("verification passes = %d, want 2: the parked one and a NEW one after the feedback", got)
	}
}

// Revise, пока после пробуждения работает автор коррекции (attempt >= 1): его feedback-ход тоже не наследует приказ.
func TestVerifyQuota_ReviseDuringCorrectionAfterWakeRunsFeedbackAuthor(t *testing.T) {
	var orch *orchestrator.Orchestrator
	correcting := make(chan struct{})
	author := newStageRunner(func(ctx context.Context, _ string, call int) error {
		if call != 2 { // call 2 — автор коррекции после needs_changes повторённого шага
			return nil
		}
		close(correcting)
		ch, ok := orchestrator.InterruptChanForTest(orch, "s1")
		if !ok {
			return errors.New("no interrupt channel for s1")
		}
		select {
		case <-ch:
			return executor.ErrUserInterrupted
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	clk := clock.NewFake(quotaStart)
	agent := &verifyAgentScript{script: func(call int) (verify.RunOutcome, error) {
		switch call {
		case 2:
			return verify.RunOutcome{}, codexLimit(clk.Now().Add(2 * time.Minute))
		case 3:
			return verdictOutcome(verify.VerdictNeedsChanges, "not yet"), nil
		default:
			return passOutcome("step ok"), nil
		}
	}}
	rig := startQuotaRig(t, t.TempDir(), verifyQuotaStages(twoStepSpec()), quotaConfig(), author, clk, true, agent.install,
		func(o *orchestrator.Orchestrator) { orch = o })
	rig.waitParked("s1")
	clk.Advance(5 * time.Minute)
	select {
	case <-correcting:
	case <-time.After(10 * time.Second):
		t.Fatal("the correction author did not start after the wake")
	}

	if applied, _, err := rig.orch.Revise(context.Background(), "s1", reviseText); err != nil || !applied {
		t.Fatalf("Revise = %v, %v; want applied", applied, err)
	}
	rig.waitStatus("s1", state.StatusDone)
	orchestrator.WaitAgentsForTest(rig.orch)

	prompts := author.promptsOf("s1")
	if len(prompts) != 3 {
		t.Fatalf("author calls = %d, want 3: initial, interrupted correction, one feedback run", len(prompts))
	}
	if !strings.Contains(prompts[2], reviseText) {
		t.Errorf("feedback author prompt lacks the user's feedback %q:\n%s", reviseText, prompts[2])
	}
	if got := agent.callCount(); got != 5 {
		t.Errorf("verify calls = %d, want 5: step 1, refused 2, rejected 2, then a fresh pass of both steps", got)
	}
}

// loopRig — оркестратор с одной стадией в running и припаркованным verify-шагом без Run: retry-цикл гоняется
// напрямую, а приказ пробуждения строится так же, как в wakeFromQuota.
type loopRig struct {
	t     *testing.T
	orch  *orchestrator.Orchestrator
	store *state.Store
	stage flow.Stage
	ctx   context.Context // с приказом пробуждения
}

func newLoopRig(t *testing.T) *loopRig {
	t.Helper()
	stages := verifyQuotaStages(twoStepSpec())
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: quotaConfig(),
		Prompts: orchestrator.DefaultPrompts(), Runner: authorOnce(), Clock: clock.NewFake(quotaStart),
	})
	r := &loopRig{t: t, orch: orch, store: store, stage: stages[0]}
	r.apply(bus.EvReady, bus.GuardCtx{})
	r.apply(bus.EvStartRun, bus.GuardCtx{})
	epoch := store.Snapshot().Stages["s1"].Epoch
	orchestrator.ParkVerifyForQuotaForTest(orch, r.stage, epoch, codexLimit(quotaStart.Add(time.Hour)), "v-1", 2)
	if st := store.Snapshot().Stages["s1"]; st.Status != state.StatusAwaitingQuota {
		t.Fatalf("verify park: status = %s", st.Status)
	}
	r.ctx = orchestrator.VerifyResumeCtxForTest(context.Background(), orch, "s1")
	r.apply(bus.EvQuotaReset, bus.GuardCtx{Phase: "implementation"}) // пробуждение: эпоха не меняется
	return r
}

func (r *loopRig) apply(ev bus.FSMEvent, g bus.GuardCtx) {
	r.t.Helper()
	if _, _, _, ok, err := bus.NewFSM(r.store).Apply("s1", ev, g, ""); err != nil || !ok {
		r.t.Fatalf("%s: ok=%v err=%v", ev, ok, err)
	}
}

// loopCounters — что сделал прогон retry-цикла.
type loopCounters struct{ author, check, handOff int }

// run гоняет цикл на ctx rig'а: автор возвращает ErrUserInterrupted, а проверка — nil, поэтому цикл заканчивается
// там, где первым упрётся: в автора (author>0) либо в проверку (check>0, затем hand-off не нужен).
func (r *loopRig) run(c *loopCounters) {
	r.t.Helper()
	orchestrator.RunRetryLoopForTest(r.ctx, r.orch, r.stage, "implementation",
		func(string) error { c.author++; return executor.ErrUserInterrupted },
		func() error { c.check++; return executor.ErrUserInterrupted },
		func() { c.handOff++ })
}

// Revise выиграл CAS между пробуждением и первой итерацией цикла: проснувшийся цикл устарел — он не идёт в verify по
// приказу, а передаёт стадию feedback-ходу; тот (тот же ctx) приказа не наследует и вызывает автора.
func TestVerifyQuota_ReviseBetweenWakeAndFirstIterationHandsOverToFeedbackRun(t *testing.T) {
	r := newLoopRig(t)
	r.apply(bus.EvRevise, bus.GuardCtx{ExpectFrom: state.StatusRunning})

	var woken loopCounters
	r.run(&woken)
	if woken.author != 0 || woken.check != 0 {
		t.Errorf("stale woken loop ran work: author=%d check=%d, want none", woken.author, woken.check)
	}
	if woken.handOff != 1 {
		t.Fatalf("hand-offs = %d, want 1: the Revise's feedback run must start", woken.handOff)
	}

	r.apply(bus.EvStartRun, bus.GuardCtx{ExpectFrom: state.StatusRevising}) // claimRevise feedback-хода
	var feedback loopCounters
	r.run(&feedback)
	if feedback.author != 1 {
		t.Errorf("feedback loop author calls = %d, want 1: the order must not skip the feedback author", feedback.author)
	}
	if feedback.check != 0 {
		t.Errorf("feedback loop reached the verify gate %d times before its author ran", feedback.check)
	}
}

// Любой другой захват стадии (Pause → Continue) тоже устаревает приказ: цикл ничего не делает и никого не запускает.
func TestVerifyQuota_PauseContinueBetweenWakeAndFirstIterationLeavesStaleLoopIdle(t *testing.T) {
	r := newLoopRig(t)
	r.apply(bus.EvPause, bus.GuardCtx{})
	r.apply(bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusRunning})

	var woken loopCounters
	r.run(&woken)
	if woken != (loopCounters{}) {
		t.Errorf("stale loop did something after Pause/Continue: %+v, want nothing", woken)
	}
}

// Приказ — одноразовый: второй цикл на том же ctx (parkForPeers → continueParkedStage(ctx) в проснувшейся горутине
// пересоздаёт ход с тем же ctx) не наследует его и начинает с автора.
func TestVerifyQuota_SecondLoopOnTheSameContextDoesNotReuseTheOrder(t *testing.T) {
	r := newLoopRig(t)

	var woken loopCounters
	r.run(&woken) // владелец приказа: автор не вызывается, цикл идёт в verify
	if woken.author != 0 || woken.check != 1 {
		t.Fatalf("order owner: author=%d check=%d, want author=0 check=1", woken.author, woken.check)
	}

	// Проверка вернула «прервано»: стадия теперь у другого хода — вернём её в running, как continueParkedStage.
	var next loopCounters
	r.run(&next)
	if next.author != 1 {
		t.Errorf("second loop on the same ctx: author calls = %d, want 1 (the order is already used)", next.author)
	}
}

// phaseRunner — runner автора, который помнит фазу каждого вызова RunAgent (implementation/review/autonomous).
type phaseRunner struct {
	mu     sync.Mutex
	phases []string
}

func (r *phaseRunner) RunPlanning(_ context.Context, _, _, outFile, _ string) error {
	return os.WriteFile(outFile, []byte(quotaPlan), 0o644)
}

func (r *phaseRunner) RunAgent(_ context.Context, phase, _, _, logFile string) error {
	r.mu.Lock()
	r.phases = append(r.phases, phase)
	r.mu.Unlock()
	stageDir := filepath.Dir(logFile)
	if err := os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("summary"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done"), 0o644)
}

func (r *phaseRunner) RunJSONQuery(context.Context, string) ([]byte, error) { return nil, nil }

func (r *phaseRunner) phasesCalled() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.phases...)
}

// seedReviewRevising готовит журнал стадии, чей ход — review Revise: стартовое восстановление revising берёт
// review-фазу из её session-файла и запускает review feedback-ход (revisingKind).
func seedReviewRevising(t *testing.T, runDir string, stage flow.Stage) {
	t.Helper()
	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(runDir, stage.ID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"plan.md":             "# plan",
		"feedback.md":         reviseText,
		"review.session.json": `{"session_id":"abc"}`,
	} {
		if err := os.WriteFile(filepath.Join(stageDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tr := range []state.Transition{
		{From: state.StatusPending, To: state.StatusReady, Event: "test_setup"},
		{From: state.StatusReady, To: state.StatusRunning, Event: "test_setup"},
		{From: state.StatusRunning, To: state.StatusRevising, Event: "test_setup"},
	} {
		tr.StageID = stage.ID
		if err := store.Apply(&tr); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()
}

// Verify-парк хранит фазу исполнения своего хода: после рестарта (runnerKind в памяти потерян) пробуждение
// возобновляет тот же review/implementation/autonomous-ход, а коррекция после needs_changes запускает автора ЭТОЙ
// фазы, а не implementation-агента по умолчанию.
func TestVerifyQuota_ParkKeepsExecutionPhaseAcrossRestart(t *testing.T) {
	spec := flow.VerifySpec{Steps: []flow.VerifyStep{codexVerifyStep}}
	cases := []struct {
		name       string
		agents     flow.AgentType
		phase      string
		seedReview bool
	}{
		{name: "implementation", agents: flow.AgentImplementation, phase: "implementation"},
		{name: "autonomous", agents: flow.AgentAuto, phase: "autonomous_execution"},
		{name: "review", agents: flow.AgentReview, phase: "review", seedReview: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runDir := t.TempDir()
			stages := []flow.Stage{{ID: "s1", Name: "S1", Agents: []flow.AgentType{c.agents}, Verify: spec}}
			if c.seedReview {
				seedReviewRevising(t, runDir, stages[0])
			}
			clk := clock.NewFake(quotaStart)
			author := &phaseRunner{}
			agent := &verifyAgentScript{script: func(call int) (verify.RunOutcome, error) {
				switch call {
				case 1:
					return verify.RunOutcome{}, codexLimit(clk.Now().Add(2 * time.Minute))
				case 2:
					return verdictOutcome(verify.VerdictNeedsChanges, "not yet"), nil
				default:
					return passOutcome("ok"), nil
				}
			}}
			rig := startQuotaRig(t, runDir, stages, quotaConfig(), author, clk, !c.seedReview, agent.install)
			q := rig.waitParked("s1")
			if q.Target != state.QuotaTargetVerify || q.VerifyPhase != c.phase {
				t.Fatalf("park = %+v, want verify target with execution phase %q", *q, c.phase)
			}
			rig.stop()

			rig2 := startQuotaRig(t, runDir, stages, quotaConfig(), author, clock.NewFake(quotaStart.Add(time.Hour)), false, agent.install)
			rig2.waitStatus("s1", state.StatusDone)
			orchestrator.WaitAgentsForTest(rig2.orch)

			want := []string{c.phase, c.phase} // ход до парка и коррекция после needs_changes
			if got := author.phasesCalled(); !slices.Equal(got, want) {
				t.Errorf("author phases = %v, want %v: the wake after the restart must keep the execution phase", got, want)
			}
			if agent.callCount() != 3 {
				t.Errorf("verify calls = %d, want 3: refused step, rejected repeat, accepted fresh pass", agent.callCount())
			}
		})
	}
}

// resetParkedOffline коммитит EvQuotaReset припаркованной стадии при остановленном оркестраторе — журнал на момент
// «процесс остановился после reset и до spawn пробуждённого хода».
func resetParkedOffline(t *testing.T, runDir string) {
	t.Helper()
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seq := store.Snapshot().Stages["s1"].Seq
	g := bus.GuardCtx{Phase: "implementation", ExpectStageSeq: &seq}
	if _, _, _, ok, err := bus.NewFSM(store).Apply("s1", bus.EvQuotaReset, g, ""); err != nil || !ok {
		t.Fatalf("quota_reset: ok=%v err=%v", ok, err)
	}
}

// parkedVerifyJournal — журнал стадии, припаркованной на verify-шаге 2 и уже разбуженной (EvQuotaReset), без
// запущенного оркестратора.
func parkedVerifyJournal(t *testing.T, agent *verifyAgentScript, author executor.Runner) string {
	t.Helper()
	runDir := t.TempDir()
	rig := startQuotaRig(t, runDir, verifyQuotaStages(twoStepSpec()), quotaConfig(), author, clock.NewFake(quotaStart), true, agent.install)
	rig.waitParked("s1")
	rig.stop()
	resetParkedOffline(t, runDir)
	return runDir
}

// Рестарт между EvQuotaReset и spawn: приказ повторить verify-шаг хранится в журнале, поэтому recovery
// повторяет ровно шаг парка и не вызывает автора.
func TestVerifyQuota_RestartAfterResetBeforeSpawnRepeatsVerifyStepWithoutAuthor(t *testing.T) {
	author := authorOnce()
	clk := clock.NewFake(quotaStart)
	agent := limitAtSecondStep(clk, 2*time.Minute)
	runDir := parkedVerifyJournal(t, agent, author)

	rig2 := startQuotaRig(t, runDir, verifyQuotaStages(twoStepSpec()), quotaConfig(), author, clock.NewFake(quotaStart.Add(time.Hour)), false, agent.install)
	rig2.waitStatus("s1", state.StatusDone)
	orchestrator.WaitAgentsForTest(rig2.orch)

	if got := author.callCount(); got != 1 {
		t.Errorf("author calls = %d, want 1: recovery after the reset must not rerun the author", got)
	}
	if got := agent.callCount(); got != 3 {
		t.Errorf("verify calls = %d, want 3: step 1, refused step 2, repeated step 2 only", got)
	}
	if p := agent.promptOf(3); !strings.Contains(p, "Step 1 (agent codex-as-claude): pass") {
		t.Errorf("repeated step must see the restored accepted step 1; prompt:\n%s", p)
	}
	if got := verifyPassDirs(t, runDir); got != 1 {
		t.Errorf("verification passes = %d, want 1: the parked pass continues, no fresh one", got)
	}
}

// Рестарт после того, как повторённый шаг уже вынес вердикт (manifest шага больше не awaiting_quota): приказа
// нет — recovery обычный, автор запускается, verify идёт свежим проходом.
func TestVerifyQuota_RestartAfterSettledStepRecoversWithAuthor(t *testing.T) {
	author := authorOnce()
	clk := clock.NewFake(quotaStart)
	agent := limitAtSecondStep(clk, 2*time.Minute)
	runDir := parkedVerifyJournal(t, agent, author)

	manifests, err := filepath.Glob(filepath.Join(runDir, "s1", "verify", "*", "manifest.json"))
	if err != nil || len(manifests) != 1 {
		t.Fatalf("manifests = %v, err = %v; want exactly the parked pass", manifests, err)
	}
	data, err := os.ReadFile(manifests[0])
	if err != nil {
		t.Fatal(err)
	}
	settled := strings.Replace(string(data), `"outcome": "awaiting_quota"`, `"outcome": "needs_changes"`, 1)
	if settled == string(data) {
		t.Fatalf("manifest has no awaiting_quota step:\n%s", data)
	}
	if err := os.WriteFile(manifests[0], []byte(settled), 0o644); err != nil {
		t.Fatal(err)
	}

	rig2 := startQuotaRig(t, runDir, verifyQuotaStages(twoStepSpec()), quotaConfig(), author, clock.NewFake(quotaStart.Add(time.Hour)), false, agent.install)
	rig2.waitStatus("s1", state.StatusDone)
	orchestrator.WaitAgentsForTest(rig2.orch)

	if got := author.callCount(); got != 2 {
		t.Errorf("author calls = %d, want 2: a settled verify step leaves no resume order", got)
	}
	if got := verifyPassDirs(t, runDir); got != 2 {
		t.Errorf("verification passes = %d, want 2: the parked one and a fresh pass", got)
	}
}

// Лимит провайдера, пришедший вместе с истёкшим дедлайном шага, — таймаут шага, а не парк: повторённый шаг получил бы
// свежий таймаут и мог бы пройти, спрятав медленный verify.
func TestVerifyQuota_LimitAtExpiredStepDeadlineIsTimeoutNotPark(t *testing.T) {
	runDir := t.TempDir()
	clk := clock.NewFake(quotaStart)
	spec := flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex-as-claude", Timeout: 20 * time.Millisecond}}}
	expiring := func(o *orchestrator.Orchestrator) {
		orchestrator.SetVerifyAgentRunnerForTest(o, func(ctx context.Context, _ flow.Stage, _, _, _, _ string) (verify.RunOutcome, error) {
			<-ctx.Done() // дедлайн шага истёк, и ровно в этот момент агент вернул отказ провайдера
			return verify.RunOutcome{}, codexLimit(clk.Now().Add(2 * time.Minute))
		})
	}
	rig := startQuotaRig(t, runDir, verifyQuotaStages(spec), quotaConfig(), authorOnce(), clk, true, expiring)

	rig.waitStatus("s1", state.StatusFailed)
	if got := lastTransitionReason(t, rig.store, "s1"); !strings.Contains(got, "verify timed out") {
		t.Errorf("failure reason = %q, want the step timeout", got)
	}
	if n := countEvent(journalEvents(t, runDir), bus.EvAwaitQuota); n != 0 {
		t.Errorf("await_quota count = %d, want 0: an expired step is not a usage-limit park", n)
	}
}

// Сосед: лимит при живом дедлайне шага по-прежнему паркует verify, даже если у шага задан таймаут.
func TestVerifyQuota_LimitWithLiveStepDeadlineStillParks(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	spec := flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex-as-claude", Timeout: time.Hour}}}
	agent := &verifyAgentScript{script: func(int) (verify.RunOutcome, error) {
		return verify.RunOutcome{}, codexLimit(clk.Now().Add(2 * time.Minute))
	}}
	rig := startQuotaRig(t, t.TempDir(), verifyQuotaStages(spec), quotaConfig(), authorOnce(), clk, true, agent.install)

	if q := rig.waitParked("s1"); q.Target != state.QuotaTargetVerify || q.VerifyStep != 1 {
		t.Fatalf("park = %+v, want a verify park of step 1", *q)
	}
}
