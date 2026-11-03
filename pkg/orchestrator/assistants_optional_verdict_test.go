package orchestrator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/schema"
)

const (
	neutralResult = `{"schema_version":1,"kind":"result","summary":"notes"}`
	// movedSuggestion carries a finding recommendation the local repair moves into its detail.
	movedSuggestion     = `{"schema_version":1,"kind":"result","summary":"notes","findings":[{"id":"f1","severity":"minor","detail":"x","suggested_change":"y"}]}`
	neutralStrictResult = `{"schema_version":1,"kind":"result","verdict":null,"summary":"notes","findings":null,"evidence":null,"context_version":null,"suggested_change":null,"limitations":null}`
)

func TestHelperContract_FollowsModeAndDelegation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     assistants.Mode
		delegate bool
		want     schema.Contract
	}{
		{"advisory", assistants.ModeAdvisory, false, schema.AssistantResultOnly},
		{"unset mode is advisory", "", false, schema.AssistantResultOnly},
		{"advisory delegate", assistants.ModeAdvisory, true, schema.AssistantResult},
		{"required", assistants.ModeRequired, false, schema.AssistantResultOnlyRequired},
		{"required delegate", assistants.ModeRequired, true, schema.AssistantResultRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := assistants.Task{Mode: tc.mode, ProfileSnapshot: assistants.Profile{Capabilities: assistants.Capabilities{Delegate: tc.delegate}}}
			if got := helperContract(task); got != tc.want {
				t.Fatalf("contract = %s, want %s", got, tc.want)
			}
		})
	}
}

// The prompt of a required helper shows its own schema, byte for byte, in place of the advisory one.
func TestHelperOutputContractBlock_ShowsTheTasksSchema(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	o.opts.Prompts.HelperResult = "Answer:\n" + schema.JSON(schema.AssistantResultOnly) + "\nDone."
	for _, tc := range []struct {
		mode assistants.Mode
		want schema.Contract
	}{
		{assistants.ModeAdvisory, schema.AssistantResultOnly},
		{assistants.ModeRequired, schema.AssistantResultOnlyRequired},
	} {
		block := o.outputContractBlock(assistants.Task{Mode: tc.mode})
		if !strings.Contains(block, schema.JSON(tc.want)) || strings.Count(block, `"$schema"`) != 1 {
			t.Fatalf("mode %q: block does not carry exactly the %s schema:\n%s", tc.mode, tc.want, block)
		}
	}
}

func attachWithMode(t *testing.T, o *Orchestrator, opID string, profile assistants.ProfileID, mode assistants.Mode) assistants.TaskID {
	t.Helper()
	id, err := o.Attach(context.Background(), AttachRequest{
		OperationID: opID, StageID: assistantsTestStageID, ProfileID: profile, Mode: mode,
		Instruction: "look at this", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return id
}

// A neutral answer of an advisory helper is a completed result without a verdict, whichever way the answer
// reached the commit: as is, as a native run's strict form, after a local repair or from the format agent.
func TestHelperOptionalVerdict_NeutralResultIsTheSameOnEveryPath(t *testing.T) {
	for _, tc := range []struct {
		name       string
		outcome    executor.HelperOutcome
		formatted  string
		wantSource string
	}{
		{"prompt", executor.HelperOutcome{ProcessOK: true, RawText: neutralResult}, "", sourcePrompt},
		{"native", executor.HelperOutcome{ProcessOK: true, NativeStarted: true, RawText: neutralStrictResult}, "", sourceNative},
		{"native fallback", executor.HelperOutcome{ProcessOK: true, NativeStarted: true, NativeFallback: executor.NativeSchemaFallback(nativeFallbackRejected), RawText: neutralResult}, "", sourceNative},
		{"local repair", executor.HelperOutcome{ProcessOK: true, RawText: movedSuggestion}, "", sourceRepair},
		{"format agent", executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","summary":"notes","findings":{"id":"f1","severity":"minor","detail":"x"}}`}, `{"schema_version":1,"kind":"result","summary":"notes","findings":[{"id":"f1","severity":"minor","detail":"x"}]}`, sourceFormatAgent},
		// The format agent's own native answer is strict-style: its null members are absent ones.
		{"format agent native answer", executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","summary":"notes","findings":{"id":"f1","severity":"minor","detail":"x"}}`}, `{"schema_version":1,"kind":"result","verdict":null,"summary":"notes","findings":[{"id":"f1","severity":"minor","detail":"x"}],"evidence":null,"context_version":null,"suggested_change":null,"limitations":null}`, sourceFormatAgent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newNativeHelperTestOrchestrator(t)
			o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				return tc.outcome, nil
			}
			var formatterCalls atomic.Int32
			o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
				formatterCalls.Add(1)
				return []byte(tc.formatted), nil
			}

			id := attachWithMode(t, o, "op-neutral", "reviewer", assistants.ModeAdvisory)
			task := waitForTaskStatus(t, store, id, assistants.StatusCompleted, 10*time.Second)
			o.concurrency.WaitAgents()

			if task.ResultSeq == 0 || task.LastVerdict != "" {
				t.Fatalf("result seq %d, verdict %q; want a result without verdict", task.ResultSeq, task.LastVerdict)
			}
			committed := readFileString(t, filepath.Join(o.taskDir(id), "result.json"))
			var fields map[string]any
			if err := json.Unmarshal([]byte(committed), &fields); err != nil {
				t.Fatal(err)
			}
			if _, has := fields["verdict"]; has || fields["summary"] != "notes" {
				t.Fatalf("result.json = %s, want the summary and no verdict", committed)
			}
			wantFormatterCalls := int32(0)
			if tc.formatted != "" {
				wantFormatterCalls = 1
			}
			if got := formatterCalls.Load(); got != wantFormatterCalls {
				t.Fatalf("format agent ran %d times, want %d", got, wantFormatterCalls)
			}
			if tc.wantSource != "" {
				got := readProvenance(t, filepath.Join(o.taskDir(id), "turn-1-output.recovery.json"))
				if got.Source != tc.wantSource {
					t.Fatalf("source = %s, want %s", got.Source, tc.wantSource)
				}
			}
		})
	}
}

// A required helper's answer without a verdict does not meet the contract: the gate stays closed, and no format
// agent runs just to add what the model did not give.
func TestHelperOptionalVerdict_RequiredWithoutVerdictKeepsTheGateClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome executor.HelperOutcome
	}{
		{"prompt", executor.HelperOutcome{ProcessOK: true, RawText: neutralResult}},
		{"native", executor.HelperOutcome{ProcessOK: true, NativeStarted: true, RawText: neutralResult}},
		{"local repair", executor.HelperOutcome{ProcessOK: true, RawText: movedSuggestion}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newNativeHelperTestOrchestrator(t)
			o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				return tc.outcome, nil
			}
			var formatterCalls atomic.Int32
			o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
				formatterCalls.Add(1)
				return []byte(`{"schema_version":1,"kind":"result","verdict":"pass","summary":"made up"}`), nil
			}

			id := attachWithMode(t, o, "op-required", "reviewer", assistants.ModeRequired)
			task := waitForTaskStatus(t, store, id, assistants.StatusFailed, 10*time.Second)
			o.concurrency.WaitAgents()

			if !strings.Contains(task.StatusReason, "verdict") {
				t.Fatalf("reason = %q, want the missing verdict named", task.StatusReason)
			}
			if task.ResultSeq != 0 || task.LastVerdict != "" || formatterCalls.Load() != 0 {
				t.Fatalf("result seq %d, verdict %q, format agent runs %d; want none", task.ResultSeq, task.LastVerdict, formatterCalls.Load())
			}
			o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "author finished")
			if o.currentStatus(assistantsTestStageID) != state.StatusAwaitingAssistants {
				t.Fatal("a required result without verdict opened the gate")
			}
		})
	}
}

// An explicit null verdict is not an absent one: it is refused as an invalid value, whatever else is wrong with the
// answer and however it reached afm, and no format agent is run to give the answer the verdict the model did not
// write.
func TestHelperOptionalVerdict_NullVerdictIsRefusedWithoutFormatAgent(t *testing.T) {
	type outcomeCase struct {
		name    string
		outcome executor.HelperOutcome
	}
	var cases []outcomeCase
	for _, input := range nullVerdictInputs {
		cases = append(cases, outcomeCase{input.name, executor.HelperOutcome{ProcessOK: true, RawText: input.raw}})
	}
	cases = append(cases, outcomeCase{"native fallback", executor.HelperOutcome{
		ProcessOK: true, NativeFallback: executor.NativeSchemaFallback(nativeFallbackRejected), RawText: nullVerdictInputs[1].raw,
	}})
	for _, mode := range []assistants.Mode{assistants.ModeAdvisory, assistants.ModeRequired} {
		for _, tc := range cases {
			t.Run(string(mode)+"/"+tc.name, func(t *testing.T) {
				o, store := newNativeHelperTestOrchestrator(t)
				o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
					return tc.outcome, nil
				}
				var formatterCalls atomic.Int32
				o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
					formatterCalls.Add(1)
					return []byte(nullVerdictNeutral), nil
				}

				id := attachWithMode(t, o, "op-null", "reviewer", mode)
				task := waitForTaskStatus(t, store, id, assistants.StatusFailed, 10*time.Second)
				o.concurrency.WaitAgents()

				if !strings.Contains(task.StatusReason, contractNotMet) || !strings.Contains(task.StatusReason, "verdict value null") || task.ResultSeq != 0 || task.LastVerdict != "" || formatterCalls.Load() != 0 {
					t.Fatalf("reason %q, result seq %d, verdict %q, format agent runs %d; want a contract failure naming the null verdict and no format agent",
						task.StatusReason, task.ResultSeq, task.LastVerdict, formatterCalls.Load())
				}
				if mode == assistants.ModeRequired {
					o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "author finished")
					if o.currentStatus(assistantsTestStageID) != state.StatusAwaitingAssistants {
						t.Fatal("a refused required result opened the gate")
					}
				}
			})
		}
	}
}

// A native run's strict-style null verdict is normalised away only for the answer's own validation; the format
// agent's eligibility is judged on the model's own bytes. So a native answer that still needs a format agent for
// another defect fails closed instead of losing its null verdict there.
func TestHelperOptionalVerdict_NativeNullVerdictWithAnotherDefectFailsClosed(t *testing.T) {
	o, store := newNativeHelperTestOrchestrator(t)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, NativeStarted: true, RawText: nullVerdictInputs[1].raw}, nil
	}
	var formatterCalls atomic.Int32
	o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
		formatterCalls.Add(1)
		return []byte(nullVerdictFindings), nil
	}

	id := attachWithMode(t, o, "op-native-null", "reviewer", assistants.ModeAdvisory)
	task := waitForTaskStatus(t, store, id, assistants.StatusFailed, 10*time.Second)
	o.concurrency.WaitAgents()

	if task.ResultSeq != 0 || formatterCalls.Load() != 0 {
		t.Fatalf("result seq %d, format agent runs %d; want no result and no format agent", task.ResultSeq, formatterCalls.Load())
	}
}

// The format agent of a required helper is held to the same lossless comparison as the advisory one: the answer's
// finding-level recommendation may move into its detail, nothing else changes.
func TestHelperOptionalVerdict_RequiredFormatAgentMayMoveAFindingSuggestion(t *testing.T) {
	// The null member keeps the answer from a local repair, so only the format agent can fix it.
	const (
		raw       = `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"notes","findings":[{"id":"f1","severity":"minor","detail":"x","suggested_change":"y"}],"limitations":null}`
		formatted = `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"notes","findings":[{"id":"f1","severity":"minor","detail":"x\n\nSuggested change:\ny"}]}`
	)
	o, store := newNativeHelperTestOrchestrator(t)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: raw}, nil
	}
	o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
		return []byte(formatted), nil
	}

	id := attachWithMode(t, o, "op-required-format", "reviewer", assistants.ModeRequired)
	task := waitForTaskStatus(t, store, id, assistants.StatusCompleted, 10*time.Second)
	o.concurrency.WaitAgents()

	if task.LastVerdict != assistants.VerdictNeedsChanges {
		t.Fatalf("verdict = %q, want needs_changes", task.LastVerdict)
	}
	if got := readProvenance(t, filepath.Join(o.taskDir(id), "turn-1-output.recovery.json")); got.Source != sourceFormatAgent {
		t.Fatalf("source = %s, want %s", got.Source, sourceFormatAgent)
	}
}

// The commit-time check stands on its own: an answer that reaches the commit without a verdict (the validator
// handed it over) still does not meet a required task's contract.
func TestCommitHelperResult_RequiredGateRejectsAVerdictlessAnswerByItself(t *testing.T) {
	o, store := newNativeHelperTestOrchestrator(t)
	o.runHelperAgent = func(ctx context.Context, taskID, _, _, _, _, _ string) (executor.HelperOutcome, error) {
		running := store.Snapshot().Assistants.Tasks[assistants.TaskID(taskID)]
		turn := helperTurn{
			exec: o.taskExecution(store.Snapshot(), running.Task), dir: o.taskDir(running.ID),
			number: running.Turn, holdEpoch: running.HoldEpoch,
		}
		end := helperTurnEnd{consumed: running.Turn, outcome: executor.HelperOutcome{ProcessOK: true, RawText: neutralResult}}
		end.recovery.Candidate = []byte(neutralResult)
		o.commitHelperResult(ctx, running.ID, running.Task, turn, end)
		return executor.HelperOutcome{ProcessErr: context.Canceled}, nil
	}

	id := attachWithMode(t, o, "op-commit-gate", "reviewer", assistants.ModeRequired)
	task := waitForTaskStatus(t, store, id, assistants.StatusFailed, 10*time.Second)
	o.concurrency.WaitAgents()

	if !strings.Contains(task.StatusReason, "verdict") || task.ResultSeq != 0 || task.LastVerdict != "" {
		t.Fatalf("reason %q, result seq %d, verdict %q; want the missing verdict named and no result", task.StatusReason, task.ResultSeq, task.LastVerdict)
	}
}

// A delegating required helper still may delegate: the verdict belongs to the result form only.
func TestHelperOptionalVerdict_RequiredDelegateFormNeedsNoVerdict(t *testing.T) {
	o, store := newNativeHelperTestOrchestrator(t)
	o.runHelperAgent = func(_ context.Context, _, profileID, _, _, _, _ string) (executor.HelperOutcome, error) {
		if profileID == "delegator" {
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		}
		return passResultOutcome(), nil
	}

	id := attachWithMode(t, o, "op-delegate", "delegator", assistants.ModeRequired)
	waitForTaskStatus(t, store, id, assistants.StatusWaitingChildren, 10*time.Second)
	o.concurrency.WaitAgents()
}

func TestHelperNativeRerun_ByTaskContract(t *testing.T) {
	const (
		forcedNeutral  = `{"schema_version":1,"kind":"result","verdict":null,"summary":"forced","findings":null}`
		forcedApproval = `{"schema_version":1,"kind":"result","verdict":"pass","summary":"forced"}`
		forcedIssue    = `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"forced"}`
	)
	for _, tc := range []struct {
		name     string
		contract schema.Contract
		payload  string
		want     string
	}{
		{"advisory forced neutral stands", schema.AssistantResultOnly, forcedNeutral, ""},
		{"required forced neutral reruns", schema.AssistantResultOnlyRequired, forcedNeutral, nativeFallbackEnforced},
		{"advisory forced pass the model did not give reruns", schema.AssistantResultOnly, forcedApproval, nativeFallbackEnforced},
		{"required forced pass the model did not give reruns", schema.AssistantResultOnlyRequired, forcedApproval, nativeFallbackEnforced},
		{"advisory forced non-pass stands", schema.AssistantResultOnly, forcedIssue, ""},
		{"required forced non-pass stands", schema.AssistantResultOnlyRequired, forcedIssue, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome := executor.HelperOutcome{ProcessOK: true, NativeStarted: true, NativeEnforced: true, StructuredPayload: true, RawText: tc.payload, TextBeforeEnforcement: "done reviewing"}
			if got := helperNativeRerun(outcome, func(string) bool { return false }, tc.contract); got != tc.want {
				t.Fatalf("rerun reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTurnReplyBody_VerdictSegmentOnlyWhenGiven(t *testing.T) {
	neutral := turnReplyBody("t-1", 2, assistants.Result{Summary: "checked it"})
	if strings.Contains(neutral, "verdict") || !strings.Contains(neutral, "full result: task t-1, turn 2") {
		t.Fatalf("neutral body = %q, want the summary and the result pointer without a verdict", neutral)
	}
	findings := turnReplyBody("t-1", 2, assistants.Result{Summary: "checked it", Findings: []assistants.Finding{{ID: "f1"}}})
	if strings.Contains(findings, "verdict") || !strings.Contains(findings, "— 1 finding; full result") {
		t.Fatalf("neutral body with a finding = %q", findings)
	}
	given := turnReplyBody("t-1", 2, assistants.Result{Verdict: assistants.VerdictPass, Summary: "checked it"})
	if !strings.Contains(given, "— verdict pass; full result") {
		t.Fatalf("body with a verdict = %q", given)
	}
}
