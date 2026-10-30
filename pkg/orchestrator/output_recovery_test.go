package orchestrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/assets"
	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/schema"
)

const recoveryHelperRaw = `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"Fix this","findings":{"id":"f1","severity":"major","detail":"Keep this finding"},"evidence":["proof"],"suggested_change":"Fix it"}`
const recoveryHelperFixed = `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"Fix this","findings":[{"id":"f1","severity":"major","detail":"Keep this finding"}],"evidence":["proof"],"suggested_change":"Fix it"}`
const recoveryVerifyRaw = `{"schema_version":1,"verdict":"needs_changes","summary":"Fix this","findings":{"blocking":true,"title":"Bug","evidence":"proof","minimal_fix":"Fix it"}}`
const recoveryVerifyFixed = `{"schema_version":1,"verdict":"needs_changes","summary":"Fix this","findings":[{"blocking":true,"title":"Bug","evidence":"proof","minimal_fix":"Fix it"}]}`

func TestOutputRecovery_PreservationAndBudget(t *testing.T) {
	for _, tc := range []struct {
		name, candidate string
		accepted        bool
	}{
		{"preserved", recoveryHelperFixed, true},
		{"lost finding", strings.Replace(recoveryHelperFixed, `[{"id":"f1","severity":"major","detail":"Keep this finding"}]`, `[]`, 1), false},
		{"changed verdict", strings.Replace(recoveryHelperFixed, `"needs_changes"`, `"pass"`, 1), false},
		{"lost evidence", strings.Replace(recoveryHelperFixed, `["proof"]`, `[]`, 1), false},
		{"lost recommendation", strings.Replace(recoveryHelperFixed, `"Fix it"`, `""`, 1), false},
		{"invalid twice", `not JSON`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(recoveryHelperRaw)
			original := bytes.Clone(raw)
			calls := 0
			got, err := recoverOutput(context.Background(), schema.AssistantResult, "task/t1/turn/1", raw, assistantOutputValidator, 2, func(ctx context.Context, _ schema.Contract, prompt string, attempt int) ([]byte, error) {
				calls++
				if !strings.Contains(prompt, schema.JSON(schema.AssistantResult)) {
					t.Fatal("formatter lacks exact schema")
				}
				return []byte(tc.candidate), nil
			})
			if (err == nil) != tc.accepted {
				t.Fatalf("accepted=%v err=%v", err == nil, err)
			}
			wantCalls := 2
			if tc.accepted {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d want %d", calls, wantCalls)
			}
			if !bytes.Equal(raw, original) {
				t.Fatal("raw mutated")
			}
			if tc.accepted && (got.Provenance.Source != "model_format" || got.Provenance.Contract != schema.AssistantResult || got.Provenance.Owner != "task/t1/turn/1" || got.Provenance.Validation != "accepted") {
				t.Fatalf("provenance=%+v", got.Provenance)
			}
		})
	}
}

// Joining a finding's detail lines is a lossless shape change, like the other free-text fields.
func TestOutputRecovery_FindingDetailLinesJoinedByFormatter(t *testing.T) {
	raw := strings.Replace(recoveryHelperRaw, `"detail":"Keep this finding"`, `"detail":["Keep this","finding"]`, 1)
	candidate := strings.Replace(recoveryHelperFixed, `"detail":"Keep this finding"`, `"detail":"Keep this\nfinding"`, 1)
	got, err := recoverOutput(context.Background(), schema.AssistantResult, "t", []byte(raw), assistantOutputValidator, 1, func(context.Context, schema.Contract, string, int) ([]byte, error) {
		return []byte(candidate), nil
	})
	if err != nil || got.Provenance.Source != "model_format" {
		t.Fatalf("err=%v provenance=%+v", err, got.Provenance)
	}
}

// The formatter is told what exactly failed, not only the class of the issue.
func TestOutputRecovery_FormatterPromptNamesTheValidationError(t *testing.T) {
	raw := strings.Replace(recoveryHelperRaw, `"detail":"Keep this finding"`, `"detail":{"text":"Keep this finding"}`, 1)
	_, _, wantErr := assistantOutputValidator([]byte(raw))
	var prompt string
	_, _ = recoverOutput(context.Background(), schema.AssistantResult, "t", []byte(raw), assistantOutputValidator, 1, func(_ context.Context, _ schema.Contract, p string, _ int) ([]byte, error) {
		prompt = p
		return nil, errors.New("stop")
	})
	if wantErr == nil || !strings.Contains(prompt, "Validation error: "+wantErr.Error()) {
		t.Fatalf("formatter prompt lacks the validation error:\n%s", prompt)
	}
}

func TestOutputRecovery_RefusalsNeverCallModel(t *testing.T) {
	for _, raw := range []string{
		"", `{"schema_version":1`, strings.Repeat("x", assistants.MaxResultBytes+1),
		`{"schema_version":1,"kind":"result","verdict":"pass","verdict":"needs_changes","summary":"x"}`,
		`{"schema_version":2,"kind":"result","verdict":"pass","summary":"x"}`,
		`{"schema_version":1,"kind":"result","verdict":"pass","summary":"x","findings":[{"id":"f","severity":"blocker","detail":"blocked"}]}`,
	} {
		_, err := recoverOutput(context.Background(), schema.AssistantResult, "t", []byte(raw), assistantOutputValidator, 2, func(context.Context, schema.Contract, string, int) ([]byte, error) {
			t.Error("called model for refusal")
			return nil, nil
		})
		if err == nil {
			t.Error("accepted refusal")
		}
	}
}

func TestOutputRecovery_LocalRedesignNoModel(t *testing.T) {
	raw, err := os.ReadFile("../assistants/testdata/redesign-result.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := recoverOutput(context.Background(), schema.AssistantResult, "t", raw, assistantOutputValidator, 2, func(context.Context, schema.Contract, string, int) ([]byte, error) {
		t.Fatal("local normalization called model")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := assistants.DecodeResult(got.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	if env.Result.Verdict != assistants.VerdictNeedsChanges || len(env.Result.Findings) != 7 || got.Provenance.Source != "schema_normalization" {
		t.Fatalf("result=%+v metadata=%+v", env.Result, got.Provenance)
	}
}

func TestHelperOutputRecovery_InterruptionDiscardsCandidate(t *testing.T) {
	for _, action := range []string{"pause", "cancel", "revise"} {
		t.Run(action, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			entered, release := make(chan struct{}), make(chan struct{})
			o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
				if err := os.WriteFile(resultFile, []byte(recoveryHelperRaw), 0600); err != nil {
					return executor.HelperOutcome{}, err
				}
				return executor.HelperOutcome{ProcessOK: true, RawText: recoveryHelperRaw}, nil
			}
			o.runOutputFormat = func(ctx context.Context, cfg executor.Config, prompt, logFile, resultFile string) ([]byte, error) {
				close(entered)
				awaitRelease(t, release)
				return []byte(recoveryHelperFixed), nil
			}
			id := attachTestTask(context.Background(), t, o, "recovery")
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("formatter did not start")
			}
			if got := store.Snapshot().Assistants.Tasks[id]; got.Status != assistants.StatusRunning || got.ResultSeq != 0 {
				t.Fatalf("premature result: %+v", got)
			}
			var err error
			switch action {
			case "pause":
				err = o.PauseTask(context.Background(), id)
			case "cancel":
				err = o.CancelTask(context.Background(), id)
			case "revise":
				err = o.BumpRootGeneration(assistantsTestStageID)
			default:
				t.Fatalf("unknown action %q", action)
			}
			if err != nil {
				t.Fatal(err)
			}
			close(release)
			o.concurrency.WaitAgents()
			got := store.Snapshot().Assistants.Tasks[id]
			if got.ResultSeq != 0 {
				t.Fatalf("stale result committed: %+v", got.LastVerdict)
			}
			if _, err := os.Stat(filepath.Join(o.taskDir(id), "result.json")); !os.IsNotExist(err) {
				t.Fatalf("stale accepted result file: %v", err)
			}
			raw, _ := os.ReadFile(filepath.Join(o.taskDir(id), rawResultFileName))
			if string(raw) != recoveryHelperRaw {
				t.Fatal("raw overwritten")
			}
			if action == "pause" && got.Status != assistants.StatusQueued {
				t.Fatalf("paused status=%s", got.Status)
			}
		})
	}
}

func TestHelperOutputRecovery_RequiredGateStaysClosed(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: recoveryHelperRaw}, nil
	}
	o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
		return []byte(recoveryHelperFixed), nil
	}
	id, err := o.Attach(context.Background(), AttachRequest{OperationID: "recovery", StageID: assistantsTestStageID, ProfileID: "reviewer", Mode: assistants.ModeRequired, Instruction: "review", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	o.concurrency.WaitAgents()
	task := store.Snapshot().Assistants.Tasks[id]
	var result assistants.Result
	accepted, readErr := os.ReadFile(filepath.Join(o.taskDir(id), "result.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if json.Unmarshal(accepted, &result) != nil || task.LastVerdict != assistants.VerdictNeedsChanges || !result.Repaired {
		t.Fatalf("result=%+v status=%s", result, task.Status)
	}
	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "author finished")
	if o.currentStatus(assistantsTestStageID) != state.StatusAwaitingAssistants {
		t.Fatal("repaired rejection opened required gate")
	}
}

func TestVerifyOutputRecovery_DeadlineQuotaPause(t *testing.T) {
	for _, action := range []string{"accept", "cancel", "deadline", "quota", "pause", "epoch"} {
		t.Run(action, func(t *testing.T) {
			stage := flow.Stage{ID: verifyTestStageID}
			o, _ := newVerifyTestOrchestrator(t, stage)
			if action == "epoch" {
				store, err := state.Open(o.opts.RunDir, []string{stage.ID})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				o.opts.Store = store
				if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
					t.Fatal(err)
				}
			}
			o.fingerprintFn = func(string) (string, bool) { return "", false }
			o.runVerifyAgent = func(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error) {
				if !strings.Contains(prompt, schema.JSON(schema.VerifyResult)) {
					t.Error("verify prompt lacks schema")
				}
				if err := os.WriteFile(resultFile, []byte(recoveryVerifyRaw), 0600); err != nil {
					return verify.RunOutcome{}, err
				}
				return verify.RunOutcome{ProcessOK: true, ProtocolErr: errors.New("invalid summary shape")}, nil
			}
			timeout := 3 * time.Second
			if action == "deadline" {
				timeout = 50 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			interrupt := make(chan struct{}, 1)
			o.interruptChans.Store(stage.ID, interrupt)
			limit := &executor.UsageLimitError{}
			o.runOutputFormat = func(formatCtx context.Context, cfg executor.Config, prompt, logFile, resultFile string) ([]byte, error) {
				if _, ok := formatCtx.Deadline(); !ok {
					t.Error("formatter lost step deadline")
				}
				switch action {
				case "epoch":
					advanceRecoveryEpoch(t, o.opts.Store, stage.ID)
				case "cancel":
					cancel()
				case "deadline":
					<-formatCtx.Done()
				case "quota":
					return nil, limit
				case "pause":
					interrupt <- struct{}{}
					<-formatCtx.Done()
				default:
					// "accept" lets the formatted answer through untouched.
				}
				return []byte(recoveryVerifyFixed), nil
			}
			result, _, err := o.runVerifyAgentStep(ctx, stage, flow.VerifyStep{Command: "test"}, 1, "v1", prompts.VerifyInputs{})
			if action == "epoch" {
				dir := stagefiles.StepDir(filepath.Join(o.opts.RunDir, stage.ID), "v1", 1)
				raw, readErr := os.ReadFile(filepath.Join(dir, "raw-result.json"))
				if readErr != nil || string(raw) != recoveryVerifyRaw {
					t.Fatalf("epoch changed raw: %s %v", raw, readErr)
				}
				if _, statErr := os.Stat(filepath.Join(dir, "output.candidate.json")); !os.IsNotExist(statErr) {
					t.Fatalf("stale verify candidate persisted: %v", statErr)
				}
			}
			switch action {
			case "accept":
				if err != nil || result.Verdict != verify.VerdictNeedsChanges {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			case "quota":
				if !errors.Is(err, limit) {
					t.Fatalf("lost quota: %v", err)
				}
			case "deadline":
				var execErr *VerifyExecError
				if !errors.As(err, &execErr) || execErr.Reason != verifyTimedOutReason {
					t.Fatalf("deadline lost timeout classification: %v", err)
				}
			default:
				if !errors.Is(err, executor.ErrUserInterrupted) {
					t.Fatalf("interruption lost classification: %v", err)
				}
			}
		})
	}
}

func TestQuestionOutputRecovery_StaleQuestionOrAnswer(t *testing.T) {
	for _, change := range []string{"unchanged", "question", "answer", "epoch"} {
		t.Run(change, func(t *testing.T) {
			raw := `{"id":"q1","question":"Pick","options":"A"}`
			o, store, dir := setupMalformedTestOrch(t, raw)
			entered, release := make(chan struct{}), make(chan struct{})
			o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
				close(entered)
				awaitRelease(t, release)
				return []byte(`{"id":"q1","question":"Pick","options":["A"]}`), nil
			}
			done := o.runJSONFixAgent(*o.reg().Stage("s1"), "implementation", "q1", 1)
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("formatter did not start")
			}
			path := filepath.Join(dir, "implementation.q1.question.json")
			switch change {
			case "question":
				if err := os.WriteFile(path, []byte(`{"question":"New"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "answer":
				if err := mcp.WriteAnswerFile(dir, "implementation", "q1", "answered", false); err != nil {
					t.Fatal(err)
				}
			case "epoch":
				advanceRecoveryEpoch(t, store, "s1")
			default:
				// "unchanged" leaves the question exactly as the agent wrote it.
			}
			close(release)
			<-done
			o.concurrency.WaitAgents()
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if change == "unchanged" {
				if _, err := mcp.DecodeQuestion(got); err != nil {
					t.Fatal(err)
				}
				original, err := os.ReadFile(path + ".raw")
				if err != nil || string(original) != raw {
					t.Fatal("missing immutable raw")
				}
			} else if change == "question" {
				if string(got) != `{"question":"New"}` {
					t.Fatal("clobbered newer question")
				}
			} else if string(got) != raw {
				t.Fatal("committed stale candidate")
			}
		})
	}
}

func TestHelperOutputRecovery_QuotaKeepsTaskReachable(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: recoveryHelperRaw}, nil
	}
	o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
		return nil, helperLimit(time.Time{})
	}
	id := attachTestTask(context.Background(), t, o, "format-quota")
	task := waitForQuotaPark(t, store, id)
	if task.Status != assistants.StatusQueued || task.Quota == nil || task.ResultSeq != 0 {
		t.Fatalf("quota task=%+v", task)
	}
}

func TestHelperOutputRecovery_WatcherAndSingleCommit(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: recoveryHelperRaw}, nil
	}
	o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
		return []byte(recoveryHelperFixed), nil
	}
	id := attachWatcherTask(t, o, "format-watcher", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	o.fireWatcherTask(context.Background(), id)
	o.concurrency.WaitAgents()
	task := store.Snapshot().Assistants.Tasks[id]
	if task.Status != assistants.StatusWatching || task.LastVerdict != assistants.VerdictNeedsChanges {
		t.Fatalf("watcher=%+v", task)
	}
	count := 0
	for _, event := range store.TaskEvents(id) {
		if event.Kind == "task_result" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("result commits=%d", count)
	}
}

func TestHelperOutputRecovery_DelegateRetainsChildren(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	fixed := delegateOutcome(childJSON(assistants.ModeAdvisory)).RawText
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fixed), &object); err != nil {
		t.Fatal(err)
	}
	var children []json.RawMessage
	if err := json.Unmarshal(object["children"], &children); err != nil {
		t.Fatal(err)
	}
	object["children"] = children[0]
	raw, _ := json.Marshal(object)
	release := make(chan struct{})
	calls := 0
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		calls++
		if calls == 1 {
			return executor.HelperOutcome{ProcessOK: true, RawText: string(raw)}, nil
		}
		if calls == 2 {
			awaitRelease(t, release)
		}
		return passResultOutcome(), nil
	}
	o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
		return []byte(fixed), nil
	}
	id := attachTestTask(context.Background(), t, o, "format-delegate")
	waitForTaskStatus(t, store, id, assistants.StatusWaitingChildren, 3*time.Second)
	if len(store.Snapshot().Assistants.ByParent[id]) != 1 {
		t.Fatal("delegation lost child")
	}
	close(release)
	o.concurrency.WaitAgents()
	if got := store.Snapshot().Assistants.Tasks[id]; got.Status != assistants.StatusCompleted || got.LastVerdict != assistants.VerdictPass {
		t.Fatalf("delegate final=%+v", got)
	}
}

func TestHelperOutputRecovery_YieldNeverFormats(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	id := deriveTaskID("format-yield")
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		_, err := o.Wait(context.Background(), WaitRequest{OperationID: "format-yield-wait", Waiter: meshHelperParticipantID(id), Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "future-thread"}, Deadline: testFarDeadline()})
		return executor.HelperOutcome{ProcessOK: true}, err
	}
	o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
		t.Error("yield invoked formatter")
		return nil, nil
	}
	attachTestTask(context.Background(), t, o, "format-yield")
	o.concurrency.WaitAgents()
	if got := store.Snapshot().Assistants.Tasks[id]; got.Status != assistants.StatusWaitingPeer || got.ResultSeq != 0 {
		t.Fatalf("yield=%+v", got)
	}
}

func TestOutputRecovery_RoleSchemasAndFormatterIsolation(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	var err error
	o.opts.Prompts.HelperResult, _, err = assets.ReadPrompt("helper-result.md", "")
	if err != nil {
		t.Fatal(err)
	}
	o.opts.Prompts.HelperDelegate, _, err = assets.ReadPrompt("helper-delegate.md", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, delegate := range []bool{false, true} {
		task := assistants.Task{}
		task.ProfileSnapshot.Capabilities.Delegate = delegate
		contract := schema.AssistantResultOnly
		if delegate {
			contract = schema.AssistantResult
		}
		prompt := o.outputContractBlock(task)
		if !strings.Contains(prompt, schema.JSON(contract)) {
			t.Fatal("wrong role schema")
		}
		if delegate && strings.Contains(prompt, schema.JSON(schema.AssistantResultOnly)) {
			t.Fatal("conflicting result-only schema")
		}
	}
	cfg := o.outputFormatterConfig("helper-cmd", assistantsTestStageID)
	if cfg.MeshClient != nil || cfg.BrowserMCP != nil || cfg.StageDir != "" || cfg.Resume || cfg.SessionID != "" {
		t.Fatal("formatter inherited author capabilities")
	}
}

func TestOutputRecovery_RealQuestionKeepsContent(t *testing.T) {
	raw, err := os.ReadFile("testdata/malformed_q4.question.json")
	if err != nil {
		t.Fatal(err)
	}
	fixed := bytes.ReplaceAll(raw, []byte{'\\', '\\', '"'}, []byte{'\\', '"'})
	calls := 0
	got, err := recoverOutput(context.Background(), schema.Question, "question/q4", raw, questionOutputValidator, 1, func(context.Context, schema.Contract, string, int) ([]byte, error) { calls++; return fixed, nil })
	if err != nil {
		t.Fatal(err)
	}
	question, err := mcp.DecodeQuestion(got.Candidate)
	if err != nil || calls != 1 || len(question.Options) != 2 || !strings.Contains(question.Question, "Companion gate") {
		t.Fatalf("calls=%d question=%+v err=%v", calls, question, err)
	}
}

func advanceRecoveryEpoch(t *testing.T, store *state.Store, id string) {
	t.Helper()
	before := store.Snapshot().Stages[id]
	if err := store.Apply(&state.Transition{StageID: id, From: before.Status, To: state.StatusPaused, Event: "pause"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: id, From: state.StatusPaused, To: state.StatusRunning, Event: "continue"}); err != nil {
		t.Fatal(err)
	}
	after := store.Snapshot().Stages[id]
	if after.Epoch == before.Epoch {
		t.Fatal("takeover did not advance epoch")
	}
}

func TestHelperTurnCommit_ResultCache(t *testing.T) {
	for _, source := range []string{"local", "formatted"} {
		for _, previous := range []string{"absent", "present"} {
			for _, outcome := range []string{"committed", "pause_wins", "storage_error"} {
				t.Run(source+"/"+previous+"/"+outcome, func(t *testing.T) {
					o, store := newAssistantsTestOrchestrator(t, true)
					activateStage(t, o, assistantsTestStageID)
					previousBytes := []byte("{\n  \"summary\": \"previous accepted result\"\n}\n")
					raw := recoveryHelperFixed
					if source == "formatted" {
						raw = recoveryHelperRaw
					}
					o.runHelperAgent = func(_ context.Context, taskID, _, _, _, _, _ string) (executor.HelperOutcome, error) {
						if previous == "present" {
							if err := os.WriteFile(filepath.Join(o.taskDir(assistants.TaskID(taskID)), "result.json"), previousBytes, 0o644); err != nil {
								return executor.HelperOutcome{}, err
							}
						}
						return executor.HelperOutcome{ProcessOK: true, RawText: raw}, nil
					}
					o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
						if source == "local" {
							t.Error("valid local result invoked formatter")
						}
						return []byte(recoveryHelperFixed), nil
					}
					entered, release := make(chan struct{}), make(chan struct{})
					barrier := func() { close(entered); awaitRelease(t, release) }
					if outcome == "committed" {
						o.helperOutputCommittedHook = barrier
					} else {
						o.helperOutputCommitHook = barrier
					}
					id := attachTestTask(context.Background(), t, o, "result-cache")
					select {
					case <-entered:
					case <-time.After(3 * time.Second):
						t.Fatal("result commit barrier not reached")
					}
					resultPath := filepath.Join(o.taskDir(id), "result.json")
					turnPath := filepath.Join(o.taskDir(id), "turn-1-result.json")
					turnBytes, err := os.ReadFile(turnPath)
					if err != nil {
						t.Fatalf("per-turn result missing: %v", err)
					}
					switch outcome {
					case "committed":
						task := store.Snapshot().Assistants.Tasks[id]
						if task.Status != assistants.StatusCompleted || task.ResultSeq == 0 {
							t.Fatalf("commit barrier preceded completion: %+v", task)
						}
						latest, err := os.ReadFile(resultPath)
						if err != nil || !bytes.Equal(latest, turnBytes) {
							t.Errorf("completed result is not visible on disk: latest=%s turn=%s err=%v", latest, turnBytes, err)
						}
					case "pause_wins":
						if err := o.PauseTask(context.Background(), id); err != nil {
							t.Fatal(err)
						}
					default:
						store.CloseEventsLogForTest()
					}
					close(release)
					o.concurrency.WaitAgents()
					if outcome == "committed" {
						return
					}
					task := store.Snapshot().Assistants.Tasks[id]
					if task.ResultSeq != 0 {
						t.Errorf("rejected result was committed: %+v", task)
					}
					if outcome == "pause_wins" && task.Status != assistants.StatusQueued {
						t.Errorf("paused turn was not requeued: %+v", task)
					}
					if outcome == "storage_error" {
						var storageErr *state.StorageError
						if !errors.As(o.loadFatal(), &storageErr) {
							t.Errorf("journal failure did not end run: %v", o.loadFatal())
						}
					}
					latest, err := os.ReadFile(resultPath)
					if previous == "present" {
						if err != nil || !bytes.Equal(latest, previousBytes) {
							t.Errorf("rejected result changed previous bytes: got=%s err=%v", latest, err)
						}
					} else if !os.IsNotExist(err) {
						t.Errorf("rejected result left latest cache: got=%s err=%v", latest, err)
					}
					if after, err := os.ReadFile(turnPath); err != nil || !bytes.Equal(after, turnBytes) {
						t.Errorf("rejection changed per-turn artifact: got=%s err=%v", after, err)
					}
					if after, err := os.ReadFile(filepath.Join(o.taskDir(id), "turn-1-output.raw.json")); err != nil || string(after) != raw {
						t.Errorf("rejection changed per-turn raw output: got=%s err=%v", after, err)
					}
				})
			}
		}
	}
}

func TestHelperOutputRecovery_PauseAtCommit(t *testing.T) {
	for _, source := range []string{"local", "formatted"} {
		t.Run(source, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			raw := strings.Replace(recoveryHelperRaw, "needs_changes", "pass", 1)
			fixed := strings.Replace(recoveryHelperFixed, "needs_changes", "pass", 1)
			if source == "local" {
				raw = fixed
			}
			o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				return executor.HelperOutcome{ProcessOK: true, RawText: raw}, nil
			}
			o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
				if source == "local" {
					t.Error("valid local output invoked formatter")
				}
				return []byte(fixed), nil
			}
			entered, release := make(chan struct{}), make(chan struct{})
			o.helperOutputCommitHook = func() { close(entered); awaitRelease(t, release) }
			id, err := o.Attach(context.Background(), AttachRequest{OperationID: "commit-pause", StageID: assistantsTestStageID, ProfileID: "reviewer", Mode: assistants.ModeRequired, Instruction: "review", Actor: state.Actor{Kind: "user"}})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("commit gate not reached")
			}
			if err := o.PauseTask(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "author finished")
			close(release)
			o.concurrency.WaitAgents()
			task := store.Snapshot().Assistants.Tasks[id]
			if task.ResultSeq != 0 || task.Status != assistants.StatusQueued {
				t.Errorf("paused turn completed: %+v", task)
			}
			if o.currentStatus(assistantsTestStageID) != state.StatusAwaitingAssistants {
				t.Error("paused result opened required gate")
			}
			if _, err := os.Stat(filepath.Join(o.taskDir(id), "result.json")); !os.IsNotExist(err) {
				t.Errorf("published stale result: %v", err)
			}
		})
	}
}

func TestQuestionOutputRecovery_TakeoverAtCommit(t *testing.T) {
	raw := `{"id":"q1","question":"Pick","options":"A"}`
	o, store, dir := setupMalformedTestOrch(t, raw)
	o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
		return []byte(`{"id":"q1","question":"Pick","options":["A"]}`), nil
	}
	entered, release := make(chan struct{}), make(chan struct{})
	o.questionOutputCommitHook = func() { close(entered); awaitRelease(t, release) }
	done := o.runJSONFixAgent(*o.reg().Stage("s1"), "implementation", "q1", 1)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("commit gate not reached")
	}
	advanceRecoveryEpoch(t, store, "s1")
	close(release)
	<-done
	o.concurrency.WaitAgents()
	got, err := os.ReadFile(filepath.Join(dir, "implementation.q1.question.json"))
	if err != nil || string(got) != raw {
		t.Fatalf("old epoch replaced question: %s, %v", got, err)
	}
}

func TestHelperOutputRecovery_RawSurvivesNextTurn(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	raw, fixed := recoveryHelperRaw, recoveryHelperFixed
	o.runHelperAgent = func(_ context.Context, _, _, _, _, _, resultFile string) (executor.HelperOutcome, error) {
		err := os.WriteFile(resultFile, []byte(raw), 0600)
		return executor.HelperOutcome{ProcessOK: true, RawText: raw}, err
	}
	o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
		return []byte(fixed), nil
	}
	id := attachWatcherTask(t, o, "raw-watcher", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	for i := 1; i <= 2; i++ {
		o.fireWatcherTask(context.Background(), id)
		o.concurrency.WaitAgents()
		if task := store.Snapshot().Assistants.Tasks[id]; task.Status != assistants.StatusWatching || task.Turn != uint64(i) {
			t.Fatalf("turn not settled: %+v", task)
		}
		raw = strings.Replace(raw, "Fix this", "Fix that", 1)
		fixed = strings.Replace(fixed, "Fix this", "Fix that", 1)
	}
	for i := 1; i <= 2; i++ {
		prefix := filepath.Join(o.taskDir(id), fmt.Sprintf("turn-%d-output", i))
		saved, err := os.ReadFile(prefix + ".raw.json")
		if err != nil {
			t.Errorf("turn %d immutable raw missing: %v", i, err)
			continue
		}
		var receipt outputProvenance
		data, err := os.ReadFile(prefix + ".recovery.json")
		if err != nil || json.Unmarshal(data, &receipt) != nil {
			t.Fatalf("missing receipt: %s %v", data, err)
		}
		if receipt.RawHash != fmt.Sprintf("%x", sha256.Sum256(saved)) || receipt.Source != "model_format" || receipt.Validation != "accepted" {
			t.Fatalf("raw/receipt mismatch: %+v", receipt)
		}
		want := recoveryHelperRaw
		if i == 2 {
			want = strings.Replace(want, "Fix this", "Fix that", 1)
		}
		if string(saved) != want {
			t.Fatalf("turn %d raw overwritten", i)
		}
	}
}

func TestQuestionOutputRecovery_QuotaDoesNotSpendAttempts(t *testing.T) {
	for _, source := range []string{"", state.QuotaSourceProviderHold} {
		t.Run(source, func(t *testing.T) {
			raw := `{"id":"q1","question":"Pick","options":"A"}`
			o, _, dir := setupMalformedTestOrchNI(t, raw)
			clk := clock.NewFake(time.Now())
			o.clock = clk
			calls := 0
			o.runOutputFormat = func(_ context.Context, cfg executor.Config, _, _, _ string) ([]byte, error) {
				if cfg.NativeSchema == nil {
					t.Error("quota/provider hold spent the first native-schema attempt")
				}
				calls++
				if calls <= 3 {
					return nil, &executor.UsageLimitError{Provider: "test", ResetAt: clk.Now().Add(time.Hour), Source: source}
				}
				return []byte(`{"id":"q1","question":"Pick","options":["A"]}`), nil
			}
			processed, malformed := map[string]bool{}, map[string]*malformedQuestionState{}
			o.pollQuestions(processed, malformed)
			for i := 1; i <= 3; i++ {
				o.pollQuestions(processed, malformed)
				o.concurrency.WaitAgents()
				o.pollQuestions(processed, malformed)
				o.concurrency.WaitAgents()
				if calls != i {
					t.Errorf("calls=%d before reset %d", calls, i)
				}
				for _, st := range malformed {
					if st.attempts != 0 {
						t.Errorf("quota spent %d attempts", st.attempts)
					}
				}
				if _, err := os.Stat(filepath.Join(dir, "implementation.q1.answer.json")); !os.IsNotExist(err) {
					t.Fatalf("quota auto-answered malformed question: %v", err)
				}
				clk.Advance(time.Hour + time.Minute)
			}
			o.pollQuestions(processed, malformed)
			o.concurrency.WaitAgents()
			got, err := os.ReadFile(filepath.Join(dir, "implementation.q1.question.json"))
			if err != nil || !mcp.CanParseQuestion(got) || calls != 4 {
				t.Fatalf("recovery after reset failed: calls=%d raw=%s err=%v", calls, got, err)
			}
		})
	}
}

func TestOutputRecovery_NumericVersionPreservation(t *testing.T) {
	for _, pair := range [][2]string{{"1.0", "1"}, {"1", "1.0"}, {"1e0", "1"}, {"1", "1e0"}} {
		for _, contract := range []schema.Contract{schema.AssistantResult, schema.VerifyResult} {
			t.Run(string(contract)+"/"+pair[0]+"-"+pair[1], func(t *testing.T) {
				raw, fixed, validator := recoveryHelperRaw, recoveryHelperFixed, outputValidator(assistantOutputValidator)
				if contract == schema.VerifyResult {
					raw, fixed, validator = recoveryVerifyRaw, recoveryVerifyFixed, verifyOutputValidator
				}
				raw = strings.Replace(raw, `"schema_version":1`, `"schema_version":`+pair[0], 1)
				fixed = strings.Replace(fixed, `"schema_version":1`, `"schema_version":`+pair[1], 1)
				got, err := recoverOutput(context.Background(), contract, "test", []byte(raw), validator, 1, func(context.Context, schema.Contract, string, int) ([]byte, error) { return []byte(fixed), nil })
				if err != nil || got.Provenance.Source != "model_format" {
					t.Fatalf("equivalent numeric spelling rejected: %v %+v", err, got.Provenance)
				}
			})
		}
	}
}

func TestHelperOutputRecovery_AncestorHoldAllowsLocalResult(t *testing.T) {
	testHelperOutputRecoveryAncestorHold(t, false)
}

func TestHelperOutputRecovery_AncestorHoldFencesFormattedResult(t *testing.T) {
	testHelperOutputRecoveryAncestorHold(t, true)
}

func testHelperOutputRecoveryAncestorHold(t *testing.T, formatted bool) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	rootID := deriveTaskID("ancestor-local")
	rootStarted, rootRelease := make(chan struct{}), make(chan struct{})
	childAtCommit, childRelease := make(chan struct{}), make(chan struct{})
	o.runHelperAgent = func(_ context.Context, taskID, _, _, _, _, _ string) (executor.HelperOutcome, error) {
		if taskID == string(rootID) {
			close(rootStarted)
			awaitRelease(t, rootRelease)
			return executor.HelperOutcome{}, nil
		}
		if formatted {
			return executor.HelperOutcome{ProcessOK: true, RawText: recoveryHelperRaw}, nil
		}
		return passResultOutcome(), nil
	}
	o.helperOutputCommitHook = func() { close(childAtCommit); awaitRelease(t, childRelease) }
	o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
		if !formatted {
			t.Error("local result invoked formatter")
		}
		return []byte(recoveryHelperFixed), nil
	}
	attachTestTask(context.Background(), t, o, "ancestor-local")
	select {
	case <-rootStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("root did not start")
	}
	children, err := o.admitChildren(context.Background(), "ancestor-local-child", rootID, oneChild(), state.Actor{Kind: "assistant", ID: string(rootID)}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-childAtCommit:
	case <-time.After(3 * time.Second):
		t.Fatal("child did not reach commit")
	}
	if formatted {
		// A journal-only hold isolates the effective-hold guard from runCtx cancellation.
		effect, err := taskHoldEffect("task_hold_set", rootID, holdOwnerManual, "ancestor held")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ApplyRunOperation(state.Operation{ID: "ancestor-hold", Kind: "test_hold", Effects: []state.Effect{effect}}, nil); err != nil {
			t.Fatal(err)
		}
	} else if err := o.PauseTask(context.Background(), rootID); err != nil {
		t.Fatal(err)
	}
	close(childRelease)
	close(rootRelease)
	o.concurrency.WaitAgents()
	rs := store.Snapshot()
	child := rs.Assistants.Tasks[children[0]]
	held, _ := rs.Assistants.EffectivelyHeld(children[0])
	if !held || len(child.Holds) != 0 {
		t.Fatalf("fixture must have only an ancestor hold: %+v", child)
	}
	if formatted {
		if child.Status != assistants.StatusQueued || child.ResultSeq != 0 {
			t.Errorf("formatted result committed under ancestor hold: %+v", child)
		}
		if _, err := os.Stat(filepath.Join(o.taskDir(children[0]), "result.json")); !os.IsNotExist(err) {
			t.Errorf("published held formatted result: %v", err)
		}
		return
	}
	if child.Status != assistants.StatusCompleted || child.ResultSeq == 0 {
		t.Fatalf("ancestor hold discarded produced local result: %+v", child)
	}
	if _, err := os.Stat(filepath.Join(o.taskDir(children[0]), "result.json")); err != nil {
		t.Fatal(err)
	}
	if rs.Assistants.Tasks[rootID].Status != assistants.StatusQueued {
		t.Fatal("paused root did not remain queued")
	}
}

// formatterNativeRequests runs the formatter for a command through two attempts and returns the NativeSchema each
// attempt asked for. Both attempts return an unusable candidate so the budget is spent.
func formatterNativeRequests(t *testing.T, o *Orchestrator, cmd string, contract schema.Contract) []*executor.NativeSchema {
	t.Helper()
	var requests []*executor.NativeSchema
	o.runOutputFormat = func(_ context.Context, cfg executor.Config, _, _, _ string) ([]byte, error) {
		requests = append(requests, cfg.NativeSchema)
		return []byte("not JSON"), nil
	}
	cfg := o.outputFormatterConfig(cmd, assistantsTestStageID)
	run := o.outputFormatter(cfg, t.TempDir(), "output", "")
	for attempt := 1; attempt <= outputFormatAttempts; attempt++ {
		if _, err := run(context.Background(), contract, "prompt", attempt); err != nil {
			t.Fatal(err)
		}
	}
	return requests
}

func TestOutputFormatter_NativeSchemaOnFirstAttemptOnly(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	o.opts.Config.Docker.Agents = map[string]config.AgentRecipe{
		"codex-recipe": {Type: config.RecipeTypeCodex},
		"glm51":        {Type: config.ClaudeCommand},
		"openai-text":  {Type: config.RecipeTypeOpenAI},
	}
	for _, tc := range []struct {
		name, cmd string
		contract  schema.Contract
		runtime   executor.SchemaRuntime // "" = schema in the prompt only
	}{
		{"claude", "claude", schema.VerifyResult, executor.SchemaRuntimeClaude},
		{"claude question", "claude", schema.Question, executor.SchemaRuntimeClaude},
		{"empty command is claude", "", schema.VerifyResult, executor.SchemaRuntimeClaude},
		{"codex shim", "codex-as-claude", schema.AssistantResult, executor.SchemaRuntimeCodex},
		{"codex recipe", "codex-recipe", schema.VerifyResult, executor.SchemaRuntimeCodex},
		// A claude recipe is requested like claude: the executor itself rejects wrappers it has not verified.
		{"claude recipe", "glm51", schema.VerifyResult, executor.SchemaRuntimeClaude},
		{"text-only recipe", "openai-text", schema.VerifyResult, ""},
		{"unknown command", "helper-cmd", schema.VerifyResult, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := formatterNativeRequests(t, o, tc.cmd, tc.contract)
			if len(requests) != outputFormatAttempts {
				t.Fatalf("formatter ran %d times", len(requests))
			}
			if requests[1] != nil {
				t.Fatalf("second attempt asked for native schema: %+v", requests[1])
			}
			if tc.runtime == "" {
				if requests[0] != nil {
					t.Fatalf("unsupported runtime asked for native schema: %+v", requests[0])
				}
				return
			}
			want := executor.NativeSchema{Runtime: tc.runtime, Schema: schema.JSON(tc.contract)}
			if requests[0] == nil || *requests[0] != want {
				t.Fatalf("first attempt = %+v, want the %s contract schema for %s", requests[0], tc.contract, tc.runtime)
			}
		})
	}
}

func TestOutputFormatterConfig_NeverCarriesNativeSchema(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	if cfg := o.outputFormatterConfig("claude", assistantsTestStageID); cfg.NativeSchema != nil {
		t.Fatalf("base formatter config carries native schema: %+v", cfg.NativeSchema)
	}
}

func TestOutputRecovery_RequestedNativeNotApplied(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		contract      schema.Contract
		provider      bool
		wantNative    bool
	}{
		{"claude helper", "claude", schema.AssistantResult, false, false},
		{"claude result only", "claude", schema.AssistantResultOnly, false, false},
		{"claude verify", "claude", schema.VerifyResult, false, false},
		{"claude question native", "claude", schema.Question, false, true},
		{"codex helper", "codex-as-claude", schema.AssistantResult, false, false},
		{"codex result only", "codex-as-claude", schema.AssistantResultOnly, false, false},
		{"codex verify", "codex-as-claude", schema.VerifyResult, false, false},
		{"codex question", "codex-as-claude", schema.Question, false, false},
		{"unverified command", "glm51", schema.Question, false, false},
		{"unverified provider", "claude", schema.Question, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			callsFile := filepath.Join(dir, "calls")
			t.Setenv("OUTPUT_FORMAT_CALLS", callsFile)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CLAUDE_CONFIG_DIR", dir)
			for _, name := range []string{"ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
				t.Setenv(name, "")
			}
			if tc.provider {
				t.Setenv("ANTHROPIC_BASE_URL", "https://provider.example/anthropic")
			}
			script := `#!/bin/sh
mode=plain
for arg do
  if [ "$arg" = --json-schema ]; then mode=native; fi
done
printf '%s\n' "$mode" >> "$OUTPUT_FORMAT_CALLS"
echo '{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":400,"result":"API Error: 400 schema rejected"}'
exit 1
`
			if err := os.WriteFile(filepath.Join(dir, tc.command), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			o, _ := newAssistantsTestOrchestrator(t, true)
			o.opts.Config.Docker.Agents = map[string]config.AgentRecipe{"glm51": {Type: config.ClaudeCommand}}
			cfg := o.outputFormatterConfig(tc.command, assistantsTestStageID)
			cfg.Dir = dir
			calls := 0
			o.runOutputFormat = func(ctx context.Context, cfg executor.Config, prompt, logFile, resultFile string) ([]byte, error) {
				calls++
				if calls == 1 && cfg.NativeSchema == nil {
					t.Fatal("first call did not request native mode")
				}
				return execOutputFormat(ctx, cfg, prompt, logFile, resultFile)
			}
			raw, validate := recoveryHelperRaw, outputValidator(assistantOutputValidator)
			switch tc.contract {
			case schema.VerifyResult:
				raw, validate = recoveryVerifyRaw, verifyOutputValidator
			case schema.Question:
				raw, validate = `{"question":"Pick","options":"one"}`, questionOutputValidator
			default:
				// The assistant result keeps the initial raw text and validator.
			}
			got, err := recoverOutput(context.Background(), tc.contract, "t", []byte(raw), validate, outputFormatAttempts, o.outputFormatter(cfg, dir, "output", ""))
			var runErr *outputRunError
			var processErr *exec.ExitError
			if !errors.As(err, &runErr) || !errors.As(err, &processErr) || processErr.ExitCode() != 1 || got.Provenance.Attempts != 1 {
				t.Fatalf("err=%v provenance=%+v", err, got.Provenance)
			}
			wantCalls, wantModes := 1, "plain\n"
			if tc.wantNative {
				wantCalls, wantModes = 2, "native\nplain\n"
			}
			modes, readErr := os.ReadFile(callsFile)
			if calls != wantCalls || readErr != nil || string(modes) != wantModes {
				t.Fatalf("calls=%d modes=%q err=%v, want %d calls with %q", calls, modes, readErr, wantCalls, wantModes)
			}
		})
	}
}

func TestOutputFormatter_NativeFailureRechecksAdmission(t *testing.T) {
	o, _ := newHoldTestOrch(t, flow.Stage{ID: "a"}, flow.Stage{ID: "b"})
	until := holdNow.Add(time.Hour)
	calls := 0
	o.runOutputFormat = func(_ context.Context, cfg executor.Config, _, _, _ string) ([]byte, error) {
		calls++
		if calls != 1 || cfg.NativeSchema == nil {
			t.Fatal("formatter ran after another stage held the provider")
		}
		parkStageForTest(t, o, "a", claudePark(until))
		return nil, &nativeSchemaError{errors.New("native schema rejected")}
	}
	cfg := o.outputFormatterConfig("claude", "b")
	_, err := o.outputFormatter(cfg, t.TempDir(), "output", "")(context.Background(), schema.Question, "prompt", 1)
	var limit *executor.UsageLimitError
	if calls != 1 || !errors.As(err, &limit) || limit.Source != state.QuotaSourceProviderHold || limit.Provider != "claude" || !limit.ResetAt.Equal(until) {
		t.Fatalf("calls=%d err=%v, want provider hold until %s after one call", calls, err, until)
	}
}

func TestOutputRecovery_NativeFailureFallsBackSameAttempt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		contract   schema.Contract
		raw, fixed string
		validate   outputValidator
	}{
		{"helper", schema.AssistantResult, recoveryHelperRaw, recoveryHelperFixed, assistantOutputValidator},
		{"verify", schema.VerifyResult, recoveryVerifyRaw, recoveryVerifyFixed, verifyOutputValidator},
		{"question", schema.Question, `{"question":"Pick","options":"one"}`, `{"question":"Pick","options":["one"]}`, questionOutputValidator},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _ := newAssistantsTestOrchestrator(t, true)
			var native []bool
			var firstCtx context.Context
			var firstLog, firstResult string
			o.runOutputFormat = func(ctx context.Context, cfg executor.Config, prompt, logFile, resultFile string) ([]byte, error) {
				native = append(native, cfg.NativeSchema != nil)
				if !strings.Contains(prompt, schema.JSON(tc.contract)) {
					t.Fatal("fallback lost schema in prompt")
				}
				if cfg.NativeSchema != nil {
					firstCtx, firstLog, firstResult = ctx, logFile, resultFile
					return nil, &nativeSchemaError{&executor.TerminalResultError{APIErrorStatus: 400, Message: "schema rejected"}}
				}
				if ctx != firstCtx {
					t.Error("fallback reset the attempt timeout")
				}
				if logFile == firstLog || resultFile == firstResult {
					t.Error("fallback overwrites native failure artifacts")
				}
				return []byte(tc.fixed), nil
			}
			cfg := o.outputFormatterConfig("claude", assistantsTestStageID)
			got, err := recoverOutput(context.Background(), tc.contract, "t", []byte(tc.raw), tc.validate, 1, o.outputFormatter(cfg, t.TempDir(), "output", ""))
			if err != nil {
				t.Fatal(err)
			}
			if got.Provenance.Attempts != 1 || got.Provenance.Source != "model_format" || got.Provenance.Validation != "accepted" || string(got.Candidate) != tc.fixed {
				t.Fatalf("recovery=%+v candidate=%s", got.Provenance, got.Candidate)
			}
			if len(native) != 2 || !native[0] || native[1] {
				t.Fatalf("native calls=%v, want [true false]", native)
			}
		})
	}
}

func TestOutputRecovery_NativeFailureBothModesFail(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	nativeErr, plainErr := errors.New("exit status 1"), errors.New("plain formatter failed")
	var native []bool
	o.runOutputFormat = func(_ context.Context, cfg executor.Config, _, _, _ string) ([]byte, error) {
		native = append(native, cfg.NativeSchema != nil)
		if cfg.NativeSchema != nil {
			return nil, &nativeSchemaError{nativeErr}
		}
		return nil, plainErr
	}
	cfg := o.outputFormatterConfig("claude", assistantsTestStageID)
	got, err := recoverOutput(context.Background(), schema.AssistantResult, "t", []byte(recoveryHelperRaw), assistantOutputValidator, outputFormatAttempts, o.outputFormatter(cfg, t.TempDir(), "output", ""))
	var runErr *outputRunError
	if !errors.As(err, &runErr) || !errors.Is(err, plainErr) || got.Provenance.Attempts != 1 {
		t.Fatalf("err=%v provenance=%+v", err, got.Provenance)
	}
	if len(native) != 2 || !native[0] || native[1] {
		t.Fatalf("native calls=%v, want [true false]", native)
	}
}

func TestOutputFormatter_NativeFailureKeepsControlErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		cancel bool
	}{
		{"usage", &executor.UsageLimitError{Message: "quota"}, false},
		{"admission", &executor.UsageLimitError{Source: state.QuotaSourceProviderHold}, false},
		{"interrupt", executor.ErrUserInterrupted, false},
		{"idle timeout", executor.ErrIdleTimeout, false},
		{"context canceled", context.Canceled, false},
		{"context deadline", context.DeadlineExceeded, false},
		{"canceled during native call", errors.New("exit status 1"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _ := newAssistantsTestOrchestrator(t, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			o.runOutputFormat = func(_ context.Context, cfg executor.Config, _, _, _ string) ([]byte, error) {
				calls++
				if cfg.NativeSchema == nil {
					t.Error("control error retried without native schema")
				}
				if tc.cancel {
					cancel()
				}
				return nil, &nativeSchemaError{fmt.Errorf("wrapped: %w", tc.err)}
			}
			cfg := o.outputFormatterConfig("claude", assistantsTestStageID)
			_, err := o.outputFormatter(cfg, t.TempDir(), "output", "")(ctx, schema.Question, "prompt", 1)
			want := tc.err
			if tc.cancel {
				want = context.Canceled
			}
			if !errors.Is(err, want) || calls != 1 {
				t.Fatalf("err=%v calls=%d, want %v with one call", err, calls, want)
			}
		})
	}
}

// A candidate that satisfies the contract schema but lost a finding is rejected even when the provider enforced
// the schema, and the retry runs without the native mode.
func TestOutputFormatter_NativeCandidateStillChecked(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	lostFinding := strings.Replace(recoveryHelperFixed, `[{"id":"f1","severity":"major","detail":"Keep this finding"}]`, `[]`, 1)
	var native []bool
	o.runOutputFormat = func(_ context.Context, cfg executor.Config, _, _, _ string) ([]byte, error) {
		native = append(native, cfg.NativeSchema != nil)
		if len(native) == 1 {
			return []byte(lostFinding), nil
		}
		return []byte(recoveryHelperFixed), nil
	}
	cfg := o.outputFormatterConfig("claude", assistantsTestStageID)
	got, err := recoverOutput(context.Background(), schema.AssistantResult, "t", []byte(recoveryHelperRaw), assistantOutputValidator, outputFormatAttempts, o.outputFormatter(cfg, t.TempDir(), "output", ""))
	if err != nil {
		t.Fatal(err)
	}
	if got.Provenance.Attempts != 2 || string(got.Candidate) != recoveryHelperFixed {
		t.Fatalf("attempts=%d candidate=%s", got.Provenance.Attempts, got.Candidate)
	}
	if len(native) != 2 || !native[0] || native[1] {
		t.Fatalf("native per attempt = %v, want [true false]", native)
	}
}

func TestHelperTurnCommit_PausedTurnNeverCommits(t *testing.T) {
	for _, site := range []string{
		"local_result", "formatted_result", "delegate", "answer_failure",
		"delegate_depth", "delegate_profile", "delegate_quota", "delegate_cap",
		"protocol_error", "execution_error", "timeout", "process_failure", "formatter_exec_error",
		"quota_park", "quota_record_error", "yield_park", "watcher_final",
	} {
		for _, interleave := range []string{"pause", "pause_continue", "journal_pause_continue"} {
			t.Run(site+"/"+interleave, func(t *testing.T) {
				o, store := newAssistantsTestOrchestrator(t, true)
				activateStage(t, o, assistantsTestStageID)
				rejection := strings.HasPrefix(site, "delegate_")
				if site == "delegate_depth" || site == "delegate_cap" {
					if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 1, Source: assistants.DepthSourceDefault, Revision: "shallow"}, state.Actor{Kind: "system"}); err != nil {
						t.Fatal(err)
					}
				}
				if site == "delegate_quota" {
					limit := 1
					o.opts.Config.Collaboration.Limits.MaxTasks = &limit
				}
				id := deriveTaskID("turn-commit")
				if site == "delegate_cap" {
					o.taskDelegationRejects.Store(id, maxConsecutiveDelegationRejections-1)
				}
				firstAtCommit, firstRelease := make(chan struct{}), make(chan struct{})
				secondAtCommit, secondRelease := make(chan struct{}), make(chan struct{})
				childStarted, childRelease := make(chan struct{}), make(chan struct{})
				var releaseOnce, secondOnce, childOnce sync.Once
				t.Cleanup(func() {
					releaseOnce.Do(func() { close(firstRelease) })
					secondOnce.Do(func() { close(secondRelease) })
					childOnce.Do(func() { close(childRelease) })
				})
				var hookCalls atomic.Int32
				o.helperOutputCommitHook = func() {
					n := hookCalls.Add(1)
					if rejection {
						// Reach the rejection commit after admission has already rejected this turn.
						n--
					}
					switch n {
					case 1:
						close(firstAtCommit)
						awaitRelease(t, firstRelease)
					case 2:
						close(secondAtCommit)
						awaitRelease(t, secondRelease)
					default:
						// Later commits pass without waiting.
					}
				}
				o.runHelperAgent = func(_ context.Context, taskID, _, _, _, _, _ string) (executor.HelperOutcome, error) {
					if taskID != string(id) {
						close(childStarted)
						awaitRelease(t, childRelease)
						return passResultOutcome(), nil
					}
					if store.Snapshot().Assistants.Tasks[id].Turn == 1 {
						switch site {
						case "protocol_error":
							return executor.HelperOutcome{ProcessOK: true, ProtocolErr: errors.New("invalid terminal envelope")}, nil
						case "execution_error":
							return executor.HelperOutcome{}, errors.New("helper launch failed")
						case "timeout":
							return executor.HelperOutcome{TimedOut: true}, nil
						case "process_failure":
							return executor.HelperOutcome{ProcessErr: errors.New("helper crashed")}, nil
						case "formatter_exec_error":
							return executor.HelperOutcome{ProcessOK: true, RawText: recoveryHelperRaw}, nil
						case "quota_park":
							return limitOutcome(helperLimit(o.clk().Now().Add(24 * time.Hour)))
						case "quota_record_error":
							return limitOutcome(helperLimit(time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)))
						case "yield_park":
							deadline := o.clk().Now().Add(time.Hour)
							_, err := o.Wait(context.Background(), WaitRequest{
								OperationID: "yield-wait", Waiter: o.taskMeshParticipant(id), Deadline: &deadline,
								Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "no-messages"},
							})
							if err != nil {
								t.Errorf("register yield wait: %v", err)
							}
							return executor.HelperOutcome{ProcessOK: true}, nil
						}
					}
					if rejection && store.Snapshot().Assistants.Tasks[id].Turn == 1 {
						child := childJSON(assistants.ModeAdvisory)
						if site == "delegate_profile" {
							child = strings.Replace(child, `"reviewer"`, `"missing-profile"`, 1)
						}
						return delegateOutcome(child), nil
					}
					if site == "yield_park" {
						for waitID, wait := range store.Snapshot().Assistants.Mesh.Waits {
							if wait.Waiter == o.taskMeshParticipant(id) && wait.Status == agentcomm.WaitPending {
								if err := o.CancelWait(context.Background(), CancelWaitRequest{OperationID: "finish-retried-wait", WaitID: waitID, Actor: state.Actor{Kind: "user"}}); err != nil {
									t.Errorf("cancel wait in fresh turn: %v", err)
								}
							}
						}
					}
					switch site {
					case "formatted_result", "answer_failure":
						return executor.HelperOutcome{ProcessOK: true, RawText: recoveryHelperRaw}, nil
					case "delegate":
						if store.Snapshot().Assistants.Tasks[id].Turn <= 2 {
							return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
						}
					default:
						// Other sites finish with a pass result.
					}
					return passResultOutcome(), nil
				}
				o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
					if site == "formatter_exec_error" {
						return nil, errors.New("formatter launch failed")
					}
					if site == "answer_failure" {
						return []byte("not JSON"), nil
					}
					if site != "formatted_result" {
						t.Error("local answer invoked formatter")
					}
					return []byte(recoveryHelperFixed), nil
				}
				trigger := assistants.TriggerOneShot
				if site == "watcher_final" {
					trigger = assistants.TriggerCheckpoints
					o.taskFinalPass.Store(id, true)
				}
				_, err := o.Attach(context.Background(), AttachRequest{OperationID: "turn-commit", StageID: assistantsTestStageID, ProfileID: "reviewer", Mode: assistants.ModeRequired, Trigger: trigger, Instruction: "review", Actor: state.Actor{Kind: "user"}})
				if err != nil {
					t.Fatal(err)
				}
				if site == "watcher_final" {
					o.fireWatcherTask(context.Background(), id)
				}
				select {
				case <-firstAtCommit:
				case <-time.After(3 * time.Second):
					t.Fatal("first turn did not reach commit")
				}
				if interleave == "journal_pause_continue" {
					for _, kind := range []string{"task_hold_set", "task_hold_cleared"} {
						effect, err := taskHoldEffect(kind, id, holdOwnerManual, "test takeover")
						if err != nil {
							t.Fatal(err)
						}
						if _, err := store.ApplyRunOperation(state.Operation{ID: kind, Kind: "test_takeover", Effects: []state.Effect{effect}}, nil); err != nil {
							t.Fatal(err)
						}
					}
				} else {
					if err := o.PauseTask(context.Background(), id); err != nil {
						t.Fatal(err)
					}
					if interleave == "pause_continue" {
						if err := o.ContinueTask(context.Background(), id); err != nil {
							t.Fatal(err)
						}
					}
				}
				o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "author finished")
				releaseOnce.Do(func() { close(firstRelease) })
				if interleave == "pause" {
					// A broken delegate may spawn a held child; release all test gates before joining.
					secondOnce.Do(func() { close(secondRelease) })
					childOnce.Do(func() { close(childRelease) })
					o.concurrency.WaitAgents()
				} else {
					select {
					case <-secondAtCommit:
					case <-time.After(3 * time.Second):
						t.Fatalf("paused turn was not requeued: %+v", store.Snapshot().Assistants.Tasks[id])
					}
				}
				rs := store.Snapshot()
				task := rs.Assistants.Tasks[id]
				wantStatus, wantTurn := assistants.StatusQueued, uint64(1)
				if interleave != "pause" {
					wantStatus, wantTurn = assistants.StatusRunning, 2
				}
				if task.Status != wantStatus || task.Turn != wantTurn || task.ResultSeq != 0 || len(rs.Assistants.ByParent[id]) != 0 {
					t.Errorf("paused outcome committed: status=%s turn=%d result=%d children=%v", task.Status, task.Turn, task.ResultSeq, rs.Assistants.ByParent[id])
				}
				if task.Quota != nil {
					t.Error("superseded outcome committed a quota wait")
				}
				if site == "watcher_final" {
					if _, pending := o.taskFinalPass.Load(id); !pending {
						t.Error("rejected watcher turn consumed its final-pass marker")
					}
				}
				if o.currentStatus(assistantsTestStageID) != state.StatusAwaitingAssistants {
					t.Error("paused result opened required gate")
				}
				if _, err := os.Stat(filepath.Join(o.taskDir(id), "result.json")); !os.IsNotExist(err) {
					t.Errorf("published paused result: %v", err)
				}
				count, counted := o.taskDelegationRejects.Load(id)
				if site == "delegate_cap" {
					if !counted || count != maxConsecutiveDelegationRejections-1 {
						t.Errorf("superseded delegation changed rejection cap: %v", count)
					}
				} else if counted {
					t.Errorf("superseded delegation counted as rejection: %v", count)
				}
				if note := o.loadFollowUp(id); strings.Contains(note, "Delegation rejected") {
					t.Error("superseded delegation wrote a rejection follow-up")
				}
				if interleave == "pause" {
					if site != "watcher_final" {
						return
					}
					if err := o.ContinueTask(context.Background(), id); err != nil {
						t.Fatal(err)
					}
				}
				secondOnce.Do(func() { close(secondRelease) })
				if site == "delegate" {
					select {
					case <-childStarted:
					case <-time.After(3 * time.Second):
						t.Fatal("fresh delegation did not start its child")
					}
					delegated := store.Snapshot().Assistants.Tasks[id]
					if delegated.Turn != 2 || delegated.Status != assistants.StatusWaitingChildren {
						t.Errorf("delegation must belong to turn 2: %+v", delegated)
					}
					childOnce.Do(func() { close(childRelease) })
				}
				o.concurrency.WaitAgents()
				final := store.Snapshot().Assistants.Tasks[id]
				want := assistants.StatusCompleted
				if site == "answer_failure" {
					want = assistants.StatusFailed
				}
				finalTurn := uint64(2)
				if site == "delegate" {
					finalTurn = 3
				}
				if final.Status != want || final.Turn != finalTurn {
					t.Errorf("fresh turn did not settle: %+v", final)
				}
				if site == "watcher_final" {
					if _, pending := o.taskFinalPass.Load(id); pending {
						t.Error("committed final pass retained its marker")
					}
					if o.currentStatus(assistantsTestStageID) != state.StatusDone {
						t.Error("retried final pass did not open the required stage gate")
					}
				}
			})
		}
	}
}

func TestQuestionOutputRecovery_NativeSchemaOnceAcrossPolls(t *testing.T) {
	o, _, _ := setupMalformedTestOrch(t, `{"id":"q1","question":"Pick","options":"A"}`)
	var native []bool
	o.runOutputFormat = func(_ context.Context, cfg executor.Config, prompt, _, _ string) ([]byte, error) {
		native = append(native, cfg.NativeSchema != nil)
		if !strings.Contains(prompt, schema.JSON(schema.Question)) {
			t.Error("formatter lacks schema in prompt")
		}
		return []byte("not JSON"), nil
	}
	processed, malformed := map[string]bool{}, map[string]*malformedQuestionState{}
	o.pollQuestions(processed, malformed)
	for range maxJSONFixAttempts {
		o.pollQuestions(processed, malformed)
		o.concurrency.WaitAgents()
	}
	if len(native) != maxJSONFixAttempts {
		t.Fatalf("formatter calls=%v", native)
	}
	for i, requested := range native {
		if requested != (i == 0) {
			t.Errorf("poller attempt %d: native=%v, want %v", i+1, requested, i == 0)
		}
	}
}

// A failure to clear the stale raw answer happens after startTurn: a Pause (or Pause→Continue) in that window
// supersedes the turn, so the failure must requeue the task instead of failing it.
func TestHelperTurnPreparation_StaleRawClearFailureOfSupersededTurnRequeues(t *testing.T) {
	for _, interleave := range []string{"pause", "pause_continue", "journal_pause_continue"} {
		t.Run(interleave, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			id := deriveTaskID("prep-failure")
			blocker := filepath.Join(o.taskDir(id), rawResultFileName)
			if err := os.MkdirAll(filepath.Join(blocker, "child"), 0o755); err != nil {
				t.Fatal(err)
			}
			atCommit, release := make(chan struct{}), make(chan struct{})
			var hookOnce, releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			o.helperOutputCommitHook = func() {
				hookOnce.Do(func() {
					close(atCommit)
					awaitRelease(t, release)
				})
			}
			var runs atomic.Int32
			o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				runs.Add(1)
				return passResultOutcome(), nil
			}
			if _, err := o.Attach(context.Background(), AttachRequest{OperationID: "prep-failure", StageID: assistantsTestStageID, ProfileID: "reviewer", Mode: assistants.ModeRequired, Instruction: "review", Actor: state.Actor{Kind: "user"}}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-atCommit:
			case <-time.After(3 * time.Second):
				t.Fatal("preparation failure did not reach its commit")
			}
			if interleave == "journal_pause_continue" {
				for _, kind := range []string{"task_hold_set", "task_hold_cleared"} {
					effect, err := taskHoldEffect(kind, id, holdOwnerManual, "test takeover")
					if err != nil {
						t.Fatal(err)
					}
					if _, err := store.ApplyRunOperation(state.Operation{ID: kind, Kind: "test_takeover", Effects: []state.Effect{effect}}, nil); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if err := o.PauseTask(context.Background(), id); err != nil {
					t.Fatal(err)
				}
				if interleave == "pause_continue" {
					if err := o.ContinueTask(context.Background(), id); err != nil {
						t.Fatal(err)
					}
				}
			}
			// The retry must be able to run: the stale blocker goes away with the superseded turn.
			if err := os.RemoveAll(blocker); err != nil {
				t.Fatal(err)
			}
			releaseOnce.Do(func() { close(release) })
			o.concurrency.WaitAgents()
			task := store.Snapshot().Assistants.Tasks[id]
			if task.Status == assistants.StatusFailed {
				t.Fatalf("superseded preparation failure failed the task: turn=%d hold_epoch=%d reason=%s", task.Turn, task.HoldEpoch, task.StatusReason)
			}
			if interleave == "pause" {
				if task.Status != assistants.StatusQueued || runs.Load() != 0 {
					t.Fatalf("paused task must wait queued without running: status=%s runs=%d", task.Status, runs.Load())
				}
				if err := o.ContinueTask(context.Background(), id); err != nil {
					t.Fatal(err)
				}
				o.concurrency.WaitAgents()
				task = store.Snapshot().Assistants.Tasks[id]
			}
			if task.Status != assistants.StatusCompleted || runs.Load() != 1 {
				t.Errorf("requeued task did not complete once: status=%s runs=%d", task.Status, runs.Load())
			}
		})
	}
}

// A native call whose process never started was not rejected by the provider: a prompt-only call would fail the same way.
func TestOutputRecovery_NativeStartFailureIsNotRetried(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	for _, name := range []string{"ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		t.Setenv(name, "")
	}
	// Found on PATH, but exec fails: native mode is selected and cmd.Start returns ErrAgentStart.
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/afm-no-such-interpreter\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	o, _ := newAssistantsTestOrchestrator(t, true)
	cfg := o.outputFormatterConfig("claude", assistantsTestStageID)
	cfg.Dir = dir
	var native []bool
	o.runOutputFormat = func(ctx context.Context, cfg executor.Config, prompt, logFile, resultFile string) ([]byte, error) {
		native = append(native, cfg.NativeSchema != nil)
		return execOutputFormat(ctx, cfg, prompt, logFile, resultFile)
	}
	got, err := recoverOutput(context.Background(), schema.Question, "t", []byte(`{"question":"Pick","options":"one"}`), questionOutputValidator, outputFormatAttempts, o.outputFormatter(cfg, dir, "output", ""))
	var runErr *outputRunError
	if !errors.As(err, &runErr) || !errors.Is(err, executor.ErrAgentStart) || got.Provenance.Attempts != 1 {
		t.Fatalf("err=%v provenance=%+v", err, got.Provenance)
	}
	if len(native) != 1 || !native[0] {
		t.Fatalf("native calls=%v, want one native call and no prompt-only retry", native)
	}
}
