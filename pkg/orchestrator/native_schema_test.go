package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/agentjobs"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/schema"
)

func mustNativeVariant(t *testing.T, contract schema.Contract, dialect schema.Dialect) string {
	t.Helper()
	raw, ok := schema.NativeJSON(contract, dialect)
	if !ok {
		t.Fatalf("%s has no %s variant", contract, dialect)
	}
	return raw
}

func nativeFamilyRecipes() map[string]config.AgentRecipe {
	return map[string]config.AgentRecipe{
		"codex-recipe":  {Type: config.RecipeTypeCodex},
		"glm51":         {Type: config.ClaudeCommand},
		"implicit-glm":  {Type: ""},
		"agent-recipe":  {Type: config.RecipeTypeOpenAIAgent},
		"cursor-recipe": {Type: config.RecipeTypeCursor},
		"openai-text":   {Type: config.RecipeTypeOpenAI},
	}
}

// The runtime follows the command's family: codex takes the strict variant, claude-family the claude one and
// openai-agent the original schema its own validator checks. Delegation has no variant for the CLI modes.
func TestNativeSchemaFor_ByCommandFamily(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	o.opts.Config.Docker.Agents = nativeFamilyRecipes()
	claudeVerify := &executor.NativeSchema{Runtime: executor.SchemaRuntimeClaude, Schema: mustNativeVariant(t, schema.VerifyResult, schema.DialectClaude)}
	strictVerify := &executor.NativeSchema{Runtime: executor.SchemaRuntimeCodex, Schema: mustNativeVariant(t, schema.VerifyResult, schema.DialectStrict)}
	agentVerify := &executor.NativeSchema{Runtime: executor.SchemaRuntimeOpenAIAgent, Schema: schema.JSON(schema.VerifyResult)}
	for _, tc := range []struct {
		name, cmd string
		contract  schema.Contract
		want      *executor.NativeSchema
	}{
		{"bare claude", "claude", schema.VerifyResult, claudeVerify},
		{"empty command is claude", "", schema.VerifyResult, claudeVerify},
		{"claude recipe", "glm51", schema.VerifyResult, claudeVerify},
		{"claude recipe with an implicit type", "implicit-glm", schema.VerifyResult, claudeVerify},
		{"codex shim", "codex-as-claude", schema.VerifyResult, strictVerify},
		{"codex recipe", "codex-recipe", schema.VerifyResult, strictVerify},
		{"openai-agent recipe", "agent-recipe", schema.VerifyResult, agentVerify},
		{"bare openai-agent shim", "openai-agent-as-claude", schema.VerifyResult, agentVerify},
		{"cursor recipe", "cursor-recipe", schema.VerifyResult, nil},
		{"text-only recipe", "openai-text", schema.VerifyResult, nil},
		{"custom command", "helper-cmd", schema.VerifyResult, nil},
		{"claude result only", "claude", schema.AssistantResultOnly, &executor.NativeSchema{Runtime: executor.SchemaRuntimeClaude, Schema: mustNativeVariant(t, schema.AssistantResultOnly, schema.DialectClaude)}},
		{"codex question", "codex-as-claude", schema.Question, &executor.NativeSchema{Runtime: executor.SchemaRuntimeCodex, Schema: mustNativeVariant(t, schema.Question, schema.DialectStrict)}},
		{"claude delegation", "claude", schema.AssistantResult, nil},
		{"codex delegation", "codex-as-claude", schema.AssistantResult, nil},
		{"openai-agent delegation keeps the original", "agent-recipe", schema.AssistantResult, &executor.NativeSchema{Runtime: executor.SchemaRuntimeOpenAIAgent, Schema: schema.JSON(schema.AssistantResult)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := o.nativeSchemaFor(tc.cmd, tc.contract)
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("nativeSchemaFor(%q, %s) = %+v, want %+v", tc.cmd, tc.contract, got, tc.want)
			}
		})
	}
}

// nativeRerunNeeded is the one rule for "the provider may have refused native mode": a limit, an interrupt, a
// timeout or a cancellation is never one, and neither is a failure of a run that did not start in native mode.
func TestNativeRerunNeeded(t *testing.T) {
	for _, tc := range []struct {
		name    string
		started bool
		err     error
		want    bool
	}{
		{"native process failed", true, errors.New("exit status 1"), true},
		{"native process failed without its own error", true, runFailure(false, false, false, nil), true},
		{"prompt-only process failed", false, errors.New("exit status 1"), false},
		{"no error", true, nil, false},
		{"usage limit", true, &executor.UsageLimitError{Message: "quota"}, false},
		{"interrupt", true, runFailure(true, false, false, nil), false},
		{"idle timeout", true, runFailure(false, true, false, nil), false},
		{"cancelled", true, context.Canceled, false},
		{"deadline", true, fmt.Errorf("wrapped: %w", context.DeadlineExceeded), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativeRerunNeeded(tc.started, tc.err); got != tc.want {
				t.Fatalf("nativeRerunNeeded(%v, %v) = %v, want %v", tc.started, tc.err, got, tc.want)
			}
		})
	}
}

// Every accepted answer names how it was obtained; repair kinds stay apart from the source.
func TestOutputRecovery_ProvenanceSourceOfEveryAnswer(t *testing.T) {
	noFormatter := func(context.Context, schema.Contract, string, int) ([]byte, error) {
		t.Fatal("a locally acceptable answer called the formatter")
		return nil, nil
	}
	clean, err := recoverOutput(context.Background(), schema.VerifyResult, "o", []byte(recoveryVerifyFixed), verifyOutputValidator, 1, noFormatter)
	if err != nil || clean.Provenance.Source != sourcePrompt || clean.Provenance.RepairKind != "" {
		t.Fatalf("clean answer: provenance=%+v err=%v, want source prompt", clean.Provenance, err)
	}
	clean.noteNative(nativeOrigin{started: true, enforced: true})
	if clean.Provenance.Source != sourceNative || !clean.Provenance.NativeEnforced {
		t.Fatalf("clean native answer: provenance=%+v, want source native and the enforcement noted", clean.Provenance)
	}

	rawNewline := strings.Replace(recoveryVerifyFixed, "Fix this", "Fix\nthis", 1)
	repaired, err := recoverOutput(context.Background(), schema.VerifyResult, "o", []byte(rawNewline), verifyOutputValidator, 1, noFormatter)
	if err != nil || repaired.Provenance.Source != sourceRepair || repaired.Provenance.RepairKind == "" {
		t.Fatalf("repaired answer: provenance=%+v err=%v, want source repair with its kind", repaired.Provenance, err)
	}
	repaired.noteNative(nativeOrigin{started: true})
	if repaired.Provenance.Source != sourceRepair {
		t.Fatalf("a repaired native answer is still a repair: %+v", repaired.Provenance)
	}

	formatted, err := recoverOutput(context.Background(), schema.VerifyResult, "o", []byte(recoveryVerifyRaw), verifyOutputValidator, 1,
		func(context.Context, schema.Contract, string, int) ([]byte, error) {
			return []byte(recoveryVerifyFixed), nil
		})
	if err != nil || formatted.Provenance.Source != sourceFormatAgent {
		t.Fatalf("formatted answer: provenance=%+v err=%v, want source format_agent", formatted.Provenance, err)
	}

	rejected, err := recoverOutput(context.Background(), schema.VerifyResult, "o", []byte(`{"verdict":"maybe"}`), verifyOutputValidator, 0, nil)
	if err == nil || rejected.Provenance.Source != "" || rejected.Provenance.Validation != "rejected" {
		t.Fatalf("rejected answer: provenance=%+v err=%v, want no source", rejected.Provenance, err)
	}
}

func readProvenance(t *testing.T, path string) outputProvenance {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	var got outputProvenance
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode provenance: %v", err)
	}
	return got
}

// A clean answer gets its receipt too, so how often each source answers can be counted; the candidate is kept
// only when it differs from the raw answer.
func TestPersistOutputRecovery_WritesEveryAnswer(t *testing.T) {
	dir := t.TempDir()
	clean, err := recoverOutput(context.Background(), schema.Question, "o", []byte(`{"question":"Pick?"}`), questionOutputValidator, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistOutputRecovery(dir, "clean", clean); err != nil {
		t.Fatal(err)
	}
	if got := readProvenance(t, filepath.Join(dir, "clean.recovery.json")); got.Source != sourcePrompt || got.Validation != validationAccepted {
		t.Fatalf("clean receipt = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "clean.candidate.json")); !os.IsNotExist(err) {
		t.Fatalf("candidate kept for an unchanged answer: %v", err)
	}

	rejected, _ := recoverOutput(context.Background(), schema.Question, "o", []byte(`not json`), questionOutputValidator, 0, nil)
	if err := persistOutputRecovery(dir, "rejected", rejected); err != nil {
		t.Fatal(err)
	}
	if got := readProvenance(t, filepath.Join(dir, "rejected.recovery.json")); got.Validation != "rejected" {
		t.Fatalf("rejected receipt = %+v", got)
	}

	formatted, err := recoverOutput(context.Background(), schema.VerifyResult, "o", []byte(recoveryVerifyRaw), verifyOutputValidator, 1,
		func(context.Context, schema.Contract, string, int) ([]byte, error) {
			return []byte(recoveryVerifyFixed), nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := persistOutputRecovery(dir, "formatted", formatted); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "formatted.candidate.json")); err != nil || string(data) != recoveryVerifyFixed {
		t.Fatalf("formatted candidate = %q (%v)", data, err)
	}
}

// A codex format call in strict mode must send every property; the nulls it sends for optional free text are
// dropped before the contract checks the candidate.
func TestOutputFormatter_NativeCandidateIsNormalized(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	strictForm := strings.TrimSuffix(recoveryHelperFixed, "}") + `,"limitations":null,"context_version":null}`
	strictForm = strings.Replace(strictForm, `"evidence":["proof"]`, `"evidence":null`, 1)
	// evidence and limitations cannot be null and are dropped; context_version can be and stays.
	want := strings.TrimSuffix(strings.Replace(recoveryHelperFixed, `,"evidence":["proof"]`, "", 1), "}") + `,"context_version":null}`
	raw := strings.Replace(recoveryHelperRaw, `,"evidence":["proof"]`, "", 1)
	o.runOutputFormat = func(_ context.Context, cfg executor.Config, _, _, _ string) ([]byte, error) {
		if cfg.NativeSchema == nil || cfg.NativeSchema.Runtime != executor.SchemaRuntimeCodex {
			t.Fatalf("native request = %+v, want codex", cfg.NativeSchema)
		}
		return []byte(strictForm), nil
	}
	cfg := o.outputFormatterConfig("codex-as-claude", assistantsTestStageID)
	got, err := recoverOutput(context.Background(), schema.AssistantResultOnly, "t", []byte(raw), assistantOutputValidator, 1, o.outputFormatter(cfg, t.TempDir(), "output", ""))
	if err != nil {
		t.Fatalf("strict-form candidate rejected: %v", err)
	}
	var gotDoc, wantDoc map[string]any
	if json.Unmarshal(got.Candidate, &gotDoc) != nil || json.Unmarshal([]byte(want), &wantDoc) != nil || fmt.Sprint(gotDoc) != fmt.Sprint(wantDoc) {
		t.Fatalf("candidate = %s, want %s", got.Candidate, want)
	}
}

// verifyNativeHarness runs one agent verify step through a scripted runner, one script entry per run.
type verifyNativeHarness struct {
	o       *Orchestrator
	stepDir string
	calls   []verifyNativeCall
}

type verifyNativeCall struct {
	promptOnly bool
	deadline   time.Time
}

type verifyNativeRun struct {
	outcome verify.RunOutcome
	err     error
	raw     string // written as the run's raw result when not empty
}

func newVerifyNativeHarness(t *testing.T, runs ...verifyNativeRun) *verifyNativeHarness {
	t.Helper()
	stage := flow.Stage{ID: verifyTestStageID}
	o, _ := newVerifyTestOrchestrator(t, stage)
	o.fingerprintFn = func(string) (string, bool) { return "", false }
	h := &verifyNativeHarness{o: o, stepDir: stagefiles.StepDir(filepath.Join(o.opts.RunDir, stage.ID), "v1", 1)}
	o.runVerifyAgent = func(ctx context.Context, _ flow.Stage, _, _, logFile, resultFile string) (verify.RunOutcome, error) {
		deadline, _ := ctx.Deadline()
		h.calls = append(h.calls, verifyNativeCall{promptOnly: verifyPromptOnly(ctx), deadline: deadline})
		if len(h.calls) > len(runs) {
			t.Fatalf("verify ran %d times, want %d", len(h.calls), len(runs))
		}
		run := runs[len(h.calls)-1]
		if err := os.WriteFile(logFile, fmt.Appendf(nil, "run %d\n", len(h.calls)), 0o600); err != nil {
			t.Fatal(err)
		}
		if run.raw != "" {
			if err := os.WriteFile(resultFile, []byte(run.raw), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return run.outcome, run.err
	}
	return h
}

func (h *verifyNativeHarness) step(ctx context.Context) (verify.ModelResult, error) {
	result, _, err := h.o.runVerifyAgentStep(ctx, flow.Stage{ID: verifyTestStageID}, flow.VerifyStep{Command: "claude"}, 1, "v1", prompts.VerifyInputs{})
	return result, err
}

const (
	nativeOKJSON    = `{"schema_version":1,"verdict":"pass","summary":"looks fine"}`
	nativeIssueJSON = `{"schema_version":1,"verdict":"needs_changes","summary":"bug","findings":[{"blocking":true,"title":"bug"}]}`
)

func decodedVerify(t *testing.T, raw string) *verify.ModelResult {
	t.Helper()
	result, err := verify.DecodeModelResult([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return &result
}

// A provider refusing native mode gets one prompt-only rerun in the same step and under the same deadline. The
// step's answer is the rerun's; the native run's files stay as *.native.* evidence.
func TestVerifyNative_RejectionRerunsPromptOnlyInTheSameStep(t *testing.T) {
	h := newVerifyNativeHarness(t,
		verifyNativeRun{outcome: verify.RunOutcome{NativeStarted: true}, raw: "partial native output"},
		verifyNativeRun{outcome: verify.RunOutcome{ProcessOK: true, Result: decodedVerify(t, nativeOKJSON)}, raw: nativeOKJSON},
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	result, err := h.step(ctx)

	if err != nil || result.Verdict != verify.VerdictPass {
		t.Fatalf("result=%+v err=%v, want the prompt-only pass", result, err)
	}
	if len(h.calls) != 2 || h.calls[0].promptOnly || !h.calls[1].promptOnly || !h.calls[0].deadline.Equal(h.calls[1].deadline) {
		t.Fatalf("calls = %+v, want native then prompt-only under one deadline", h.calls)
	}
	for name, want := range map[string]string{"agent.native.log": "run 1\n", "raw-result.native.json": "partial native output", "agent.log": "run 2\n", "raw-result.json": nativeOKJSON} {
		if data, err := os.ReadFile(filepath.Join(h.stepDir, name)); err != nil || string(data) != want {
			t.Errorf("%s = %q (%v), want %q", name, data, err, want)
		}
	}
	got := readProvenance(t, filepath.Join(h.stepDir, "output.recovery.json"))
	if got.Source != sourcePrompt || got.NativeFallback != nativeFallbackRejected {
		t.Fatalf("provenance = %+v, want a prompt answer after a native rejection", got)
	}
}

// A pass the CLI forced out of a turn that ended in text is never accepted: the step reruns prompt-only.
func TestVerifyNative_EnforcedPassRerunsPromptOnly(t *testing.T) {
	h := newVerifyNativeHarness(t,
		verifyNativeRun{outcome: verify.RunOutcome{ProcessOK: true, NativeStarted: true, StructuredPayload: true, NativeEnforced: true, Result: decodedVerify(t, nativeOKJSON)}, raw: nativeOKJSON},
		verifyNativeRun{outcome: verify.RunOutcome{ProcessOK: true, Result: decodedVerify(t, nativeIssueJSON)}, raw: nativeIssueJSON},
	)

	result, err := h.step(context.Background())

	if err != nil || result.Verdict != verify.VerdictNeedsChanges {
		t.Fatalf("result=%+v err=%v, want the prompt-only verdict, not the forced pass", result, err)
	}
	if len(h.calls) != 2 || !h.calls[1].promptOnly {
		t.Fatalf("calls = %+v, want a prompt-only rerun", h.calls)
	}
	got := readProvenance(t, filepath.Join(h.stepDir, "output.recovery.json"))
	if got.Source != sourcePrompt || got.NativeFallback != nativeFallbackEnforced || !got.NativeEnforced {
		t.Fatalf("provenance = %+v, want the forced answer recorded", got)
	}
}

// A forced answer that does not decode is never handed to the formatter, which could turn it into a pass: the step
// reruns prompt-only.
func TestVerifyNative_EnforcedUndecodableAnswerRerunsPromptOnly(t *testing.T) {
	h := newVerifyNativeHarness(t,
		verifyNativeRun{outcome: verify.RunOutcome{ProcessOK: true, NativeStarted: true, StructuredPayload: true, NativeEnforced: true, ProtocolErr: errors.New("parse JSON: unexpected end of JSON input")}, raw: `{"schema_version":1,"verdict":"pass"`},
		verifyNativeRun{outcome: verify.RunOutcome{ProcessOK: true, Result: decodedVerify(t, nativeIssueJSON)}, raw: nativeIssueJSON},
	)
	h.o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
		t.Fatal("the formatter was handed the forced answer")
		return nil, nil
	}

	result, err := h.step(context.Background())

	if err != nil || result.Verdict != verify.VerdictNeedsChanges {
		t.Fatalf("result=%+v err=%v, want the prompt-only verdict", result, err)
	}
	if len(h.calls) != 2 || h.calls[0].promptOnly || !h.calls[1].promptOnly {
		t.Fatalf("calls = %+v, want native then prompt-only", h.calls)
	}
	got := readProvenance(t, filepath.Join(h.stepDir, "output.recovery.json"))
	if got.Source != sourcePrompt || got.NativeFallback != nativeFallbackEnforced || !got.NativeEnforced {
		t.Fatalf("provenance = %+v, want the forced answer recorded", got)
	}
}

// A forced answer that fails closed is kept: it can only stop the gate, never open it.
func TestVerifyNative_EnforcedFailClosedVerdictIsAccepted(t *testing.T) {
	h := newVerifyNativeHarness(t,
		verifyNativeRun{outcome: verify.RunOutcome{ProcessOK: true, NativeStarted: true, StructuredPayload: true, NativeEnforced: true, Result: decodedVerify(t, nativeIssueJSON)}, raw: nativeIssueJSON},
	)

	result, err := h.step(context.Background())

	if err != nil || result.Verdict != verify.VerdictNeedsChanges || len(h.calls) != 1 {
		t.Fatalf("result=%+v err=%v calls=%d, want the forced needs_changes kept", result, err, len(h.calls))
	}
	got := readProvenance(t, filepath.Join(h.stepDir, "output.recovery.json"))
	if got.Source != sourceNative || got.NativeFallback != "" || !got.NativeEnforced {
		t.Fatalf("provenance = %+v, want a native answer noted as forced", got)
	}
}

// Interrupts, timeouts, limits and failures of runs that never started in native mode keep their own handling.
func TestVerifyNative_ControlFailuresAreNotRerun(t *testing.T) {
	limit := &executor.UsageLimitError{Message: "quota"}
	for _, tc := range []struct {
		name string
		run  verifyNativeRun
		want func(error) bool
	}{
		{"interrupted", verifyNativeRun{outcome: verify.RunOutcome{NativeStarted: true, Interrupted: true}}, func(err error) bool { return errors.Is(err, executor.ErrUserInterrupted) }},
		{"idle timeout", verifyNativeRun{outcome: verify.RunOutcome{NativeStarted: true, TimedOut: true}}, func(err error) bool {
			var execErr *VerifyExecError
			return errors.As(err, &execErr) && execErr.Reason == verifyTimedOutReason
		}},
		{"usage limit", verifyNativeRun{err: limit}, func(err error) bool { return errors.Is(err, limit) }},
		{"prompt-only process failed", verifyNativeRun{outcome: verify.RunOutcome{}}, func(err error) bool {
			var execErr *VerifyExecError
			return errors.As(err, &execErr) && execErr.Transient
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newVerifyNativeHarness(t, tc.run)
			_, err := h.step(context.Background())
			if !tc.want(err) || len(h.calls) != 1 {
				t.Fatalf("err=%v calls=%d, want the run's own outcome without a rerun", err, len(h.calls))
			}
		})
	}
}

// An expired step deadline is a timeout, never a reason to try again prompt-only.
func TestVerifyNative_ExpiredDeadlineIsNotRerun(t *testing.T) {
	h := newVerifyNativeHarness(t, verifyNativeRun{outcome: verify.RunOutcome{NativeStarted: true}})
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := h.step(ctx)
	var execErr *VerifyExecError
	if !errors.As(err, &execErr) || execErr.Reason != verifyTimedOutReason || len(h.calls) > 1 {
		t.Fatalf("err=%v calls=%d, want a timeout without a rerun", err, len(h.calls))
	}
}

// Clean answers have provenance too: native when the run started in native mode, prompt otherwise.
func TestVerifyNative_CleanAnswerRecordsItsSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		native bool
		want   string
	}{{"native", true, sourceNative}, {"prompt", false, sourcePrompt}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newVerifyNativeHarness(t, verifyNativeRun{outcome: verify.RunOutcome{ProcessOK: true, NativeStarted: tc.native, Result: decodedVerify(t, nativeOKJSON)}, raw: nativeOKJSON})
			if _, err := h.step(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := readProvenance(t, filepath.Join(h.stepDir, "output.recovery.json")); got.Source != tc.want {
				t.Fatalf("provenance = %+v, want source %s", got, tc.want)
			}
		})
	}
}

// The production runner asks for native mode by the command's family, and not on the prompt-only rerun.
func TestVerifyNative_ExecVerifyAgentFollowsThePromptOnlyFlag(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	answer := mustJSONLine(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{{"type": "text", "text": nativeOKJSON}}}})
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + argsFile + "\necho '---' >> " + argsFile + "\ncat <<'EOF'\n" + answer + "\nEOF\necho '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	o, _ := newVerifyTestOrchestrator(t, flow.Stage{ID: verifyTestStageID})
	stage := flow.Stage{ID: verifyTestStageID}
	logDir := t.TempDir()

	native, err := o.execVerifyAgent(withVerifyStep(context.Background(), 1), stage, "claude", "verify", filepath.Join(logDir, "a.log"), filepath.Join(logDir, "a.json"))
	if err != nil || !native.NativeStarted {
		t.Fatalf("native run: outcome=%+v err=%v", native, err)
	}
	plain, err := o.execVerifyAgent(withPromptOnlyVerify(withVerifyStep(context.Background(), 1)), stage, "claude", "verify", filepath.Join(logDir, "b.log"), filepath.Join(logDir, "b.json"))
	if err != nil || plain.NativeStarted || plain.Result == nil {
		t.Fatalf("prompt-only run: outcome=%+v err=%v", plain, err)
	}
	runs := strings.Split(strings.TrimSuffix(readFileString(t, argsFile), "---\n"), "---\n")
	if len(runs) != 2 || !strings.Contains(runs[0], "--json-schema") || strings.Contains(runs[1], "--json-schema") {
		t.Fatalf("argv per run = %q, want --json-schema only on the first", runs)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// newNativeHelperTestOrchestrator registers profiles that cannot delegate, so their turns run in native mode.
func newNativeHelperTestOrchestrator(t *testing.T) (*Orchestrator, *state.Store) {
	t.Helper()
	o, store := newAssistantsTestOrchestrator(t, true)
	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{
		{ID: "reviewer", Title: "Reviewer", Command: "claude"},
		{ID: "delegator", Title: "Delegator", Command: "claude", Capabilities: assistants.Capabilities{Delegate: true}},
		{ID: "codexer", Title: "Codexer", Command: "codex-as-claude"},
		{ID: "custom", Title: "Custom", Command: "helper-cmd"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	o.opts.AssistantsRegistry = reg
	activateStage(t, o, assistantsTestStageID)
	return o, store
}

func attachNativeProfile(t *testing.T, o *Orchestrator, opID string, profile assistants.ProfileID) assistants.TaskID {
	t.Helper()
	taskID, err := o.Attach(context.Background(), AttachRequest{
		OperationID: opID, StageID: assistantsTestStageID, ProfileID: profile, Instruction: "look at this", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return taskID
}

// Only a helper that cannot delegate runs in native mode: delegation's alternatives have no native form.
func TestHelperNative_ConfigRequestsNativeOnlyWithoutDelegation(t *testing.T) {
	o, store := newNativeHelperTestOrchestrator(t)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	for _, tc := range []struct {
		name    string
		profile assistants.ProfileID
		mode    assistants.Mode
		cmd     string
		want    *executor.NativeSchema
	}{
		{"reviewer", "reviewer", assistants.ModeAdvisory, "claude", &executor.NativeSchema{Runtime: executor.SchemaRuntimeClaude, Schema: mustNativeVariant(t, schema.AssistantResultOnly, schema.DialectClaude)}},
		{"codexer", "codexer", assistants.ModeAdvisory, "codex-as-claude", &executor.NativeSchema{Runtime: executor.SchemaRuntimeCodex, Schema: mustNativeVariant(t, schema.AssistantResultOnly, schema.DialectStrict)}},
		{"delegator", "delegator", assistants.ModeAdvisory, "claude", nil},
		{"custom", "custom", assistants.ModeAdvisory, "helper-cmd", nil},
		{"required reviewer", "reviewer", assistants.ModeRequired, "claude", &executor.NativeSchema{Runtime: executor.SchemaRuntimeClaude, Schema: mustNativeVariant(t, schema.AssistantResultOnlyRequired, schema.DialectClaude)}},
		{"required codexer", "codexer", assistants.ModeRequired, "codex-as-claude", &executor.NativeSchema{Runtime: executor.SchemaRuntimeCodex, Schema: mustNativeVariant(t, schema.AssistantResultOnlyRequired, schema.DialectStrict)}},
		{"required delegator", "delegator", assistants.ModeRequired, "claude", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			taskID := attachWithMode(t, o, "op-native-"+strings.ReplaceAll(tc.name, " ", "-"), tc.profile, tc.mode)
			waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 5*time.Second)
			got := o.helperExecutorConfig(taskID, tc.cmd).NativeSchema
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("NativeSchema = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// In native mode a payload is forced even into a turn that yielded, so the turn's waits alone decide: a wait the
// turn yielded on is a yield whatever the payload, a wait that was ready at registration is not.
func TestHelperNative_TurnYieldedByWaitsAlone(t *testing.T) {
	const payload = `{"schema_version":1,"kind":"result","verdict":"pass","summary":"forced"}`
	o, _ := setupHookOrch(t, "s1")
	ctx := context.Background()
	waiter := meshStageParticipantID("s1")
	th, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-th", Members: []agentcomm.ParticipantID{waiter, meshUserParticipantID}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatal(err)
	}
	send := func(op string, sender, to agentcomm.ParticipantID, inReplyTo *agentcomm.MessageID) agentcomm.MessageID {
		t.Helper()
		kind := agentcomm.MessageKindMessage
		if inReplyTo != nil {
			kind = agentcomm.MessageKindReply
		}
		sent, err := o.Send(ctx, SendRequest{OperationID: op, ThreadID: th.ID, Sender: sender, Recipients: []agentcomm.ParticipantID{to}, Kind: kind, InReplyTo: inReplyTo, Body: op})
		if err != nil {
			t.Fatal(err)
		}
		return sent.MessageID
	}

	before := o.meshWaitStatusesOf(waiter)
	if o.turnYielded(waiter, before, payload, true) {
		t.Fatal("a native turn without a wait is an answer")
	}

	asked := send("op-ask", waiter, meshUserParticipantID, nil)
	send("op-early-answer", meshUserParticipantID, waiter, &asked)
	if _, err := o.Wait(ctx, WaitRequest{OperationID: "op-ready", Waiter: waiter, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: asked}}); err != nil {
		t.Fatal(err)
	}
	if o.turnYielded(waiter, before, payload, true) {
		t.Fatal("a wait ready at registration is no yield: the turn read its answer and went on")
	}

	// A thread of its own: the early answer above must not satisfy this wait at registration.
	late, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-th-late", Members: []agentcomm.ParticipantID{waiter, meshUserParticipantID}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatal(err)
	}
	before = o.meshWaitStatusesOf(waiter)
	if _, err := o.Wait(ctx, WaitRequest{OperationID: "op-pending", Waiter: waiter, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: late.ID}, Deadline: testFarDeadline()}); err != nil {
		t.Fatal(err)
	}
	if !o.turnYielded(waiter, before, payload, true) {
		t.Fatal("a pending wait is a yield whatever the payload")
	}
	if _, err := o.Send(ctx, SendRequest{OperationID: "op-late-answer", ThreadID: late.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{waiter}, Kind: agentcomm.MessageKindMessage, Body: "late"}); err != nil {
		t.Fatal(err)
	}
	if o.hasOpenMeshWaitFor(waiter) {
		t.Fatal("setup: the wait should be answered")
	}
	if !o.turnYielded(waiter, before, payload, true) {
		t.Fatal("a wait the turn yielded on is a yield even when answered before the exit")
	}
	if o.turnYielded(waiter, before, payload, false) {
		t.Fatal("prompt mode keeps its rule: a non-empty answer after an ended wait is the answer")
	}
}

// End to end: a native helper whose yield was answered before it exited is resumed, not committed with the
// payload the CLI forced; the same turn in prompt mode commits its answer.
func TestHelperNative_ForcedPayloadAfterAYieldIsNotCommitted(t *testing.T) {
	for _, tc := range []struct {
		name      string
		native    bool
		wantTurns int32
	}{{"native", true, 2}, {"prompt", false, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newNativeHelperTestOrchestrator(t)
			taskID := deriveTaskID("op-forced")
			helperID := meshHelperParticipantID(taskID)
			var turns atomic.Int32
			o.runHelperAgent = func(_ context.Context, _, _, _, _, _, resultFile string) (executor.HelperOutcome, error) {
				summary := "after the reply"
				if turns.Add(1) == 1 {
					registerHelperWait(t, o, helperID, deriveMeshThreadID("op-yield-open"))
					replyToHelper(t, o, helperID)
					summary = "forced"
				}
				raw := `{"schema_version":1,"kind":"result","verdict":"pass","summary":"` + summary + `"}`
				return executor.HelperOutcome{ProcessOK: true, RawText: raw, StructuredPayload: tc.native, NativeStarted: tc.native}, nil
			}
			attachNativeProfile(t, o, "op-forced", "reviewer")
			waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 5*time.Second)
			o.concurrency.WaitAgents()
			if got := turns.Load(); got != tc.wantTurns {
				t.Fatalf("turns = %d, want %d", got, tc.wantTurns)
			}
			committed := readFileString(t, filepath.Join(o.taskDir(taskID), "result.json"))
			if wantForced := !tc.native; strings.Contains(committed, "forced") != wantForced {
				t.Fatalf("committed result = %s", committed)
			}
		})
	}
}

// The job ends the way the turn does: a native helper that yielded is a yielded job even with a payload.
func TestHelperNative_JobYieldedLikeItsTurn(t *testing.T) {
	for _, tc := range []struct {
		profile assistants.ProfileID
		want    bool
	}{{"reviewer", true}, {"delegator", false}} {
		t.Run(string(tc.profile), func(t *testing.T) {
			o, store := newNativeHelperTestOrchestrator(t)
			jobs, err := agentjobs.Open(o.opts.RunDir, agentjobs.Header{RecordingStartedAt: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = jobs.Close() })
			o.opts.Jobs = jobs
			started, release := make(chan struct{}), make(chan struct{})
			o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				close(started)
				awaitRelease(t, release)
				return passResultOutcome(), nil
			}
			taskID := attachNativeProfile(t, o, "op-job-"+string(tc.profile), tc.profile)
			<-started
			helperID := meshHelperParticipantID(taskID)
			cfg := o.helperExecutorConfig(taskID, "claude")
			registerHelperWait(t, o, helperID, deriveMeshThreadID("op-yield-open"))
			replyToHelper(t, o, helperID)
			got := cfg.Job != nil && cfg.Job.Yielded != nil && cfg.Job.Yielded(`{"schema_version":1,"kind":"result","verdict":"pass","summary":"forced"}`)
			close(release)
			waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 5*time.Second)
			if got != tc.want {
				t.Fatalf("job yielded = %v, want %v", got, tc.want)
			}
		})
	}
}

// A native helper answer gets provenance like any other; the nulls of a strict-mode answer are dropped first.
func TestHelperNative_AnswerIsNormalizedAndRecorded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		native bool
		raw    string
		want   string
	}{
		{"native strict form", true, `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","findings":null,"evidence":null,"suggested_change":null,"limitations":null,"context_version":null}`, sourceNative},
		{"prompt answer", false, `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok"}`, sourcePrompt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newNativeHelperTestOrchestrator(t)
			o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				return executor.HelperOutcome{ProcessOK: true, RawText: tc.raw, NativeStarted: tc.native}, nil
			}
			taskID := attachNativeProfile(t, o, "op-prov", "reviewer")
			tk := waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 5*time.Second)
			if tk.LastVerdict != assistants.VerdictPass {
				t.Fatalf("task = %+v", tk)
			}
			got := readProvenance(t, filepath.Join(o.taskDir(taskID), "turn-1-output.recovery.json"))
			if got.Source != tc.want || got.Contract != schema.AssistantResultOnly {
				t.Fatalf("provenance = %+v, want source %s", got, tc.want)
			}
			if committed := readFileString(t, filepath.Join(o.taskDir(taskID), "result.json")); strings.Contains(committed, `"evidence":null`) {
				t.Fatalf("committed result keeps a null the contract rejects: %s", committed)
			}
			// Only the candidate is normalized: the record keeps the model's own bytes.
			if rawFile := readFileString(t, filepath.Join(o.taskDir(taskID), "turn-1-output.raw.json")); rawFile != tc.raw {
				t.Fatalf("immutable raw = %s, want the answer as sent %s", rawFile, tc.raw)
			}
			if want := fmt.Sprintf("%x", sha256.Sum256([]byte(tc.raw))); got.RawHash != want {
				t.Fatalf("raw hash = %s, want the hash of the answer as sent %s", got.RawHash, want)
			}
		})
	}
}

// The size cap judges the answer as sent: dropping the nulls and spaces of an oversized native answer must not
// shrink it under the cap.
func TestHelperNative_OversizedAnswerIsRejectedAsSent(t *testing.T) {
	o, store := newNativeHelperTestOrchestrator(t)
	raw := strings.Repeat(" ", assistants.MaxResultBytes) + `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","evidence":null}`
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: raw, StructuredPayload: true, NativeStarted: true}, nil
	}

	taskID := attachNativeProfile(t, o, "op-oversized", "reviewer")

	tk := waitForTaskStatus(t, store, taskID, assistants.StatusFailed, 5*time.Second)
	if !strings.Contains(tk.StatusReason, "byte limit") {
		t.Fatalf("reason = %q, want the size cap", tk.StatusReason)
	}
	if got := readFileString(t, filepath.Join(o.taskDir(taskID), "turn-1-output.raw.json")); got != raw {
		t.Fatalf("immutable raw has %d bytes, want the %d bytes sent", len(got), len(raw))
	}
}

// A native format candidate over the cap stays over it: normalization never shrinks it into acceptance.
func TestOutputFormatter_OversizedNativeCandidateIsRejected(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	strictForm := strings.Repeat(" ", assistants.MaxResultBytes) + strings.Replace(recoveryHelperFixed, `"evidence":["proof"]`, `"evidence":null`, 1)
	raw := strings.Replace(recoveryHelperRaw, `,"evidence":["proof"]`, "", 1)
	o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
		return []byte(strictForm), nil
	}
	cfg := o.outputFormatterConfig("codex-as-claude", assistantsTestStageID)

	got, err := recoverOutput(context.Background(), schema.AssistantResultOnly, "t", []byte(raw), assistantOutputValidator, 1, o.outputFormatter(cfg, t.TempDir(), "output", ""))

	if err == nil || got.Candidate != nil {
		t.Fatalf("oversized candidate accepted: candidate=%s err=%v", got.Candidate, err)
	}
}

// A provider that refuses native mode costs a helper one prompt-only rerun of the same turn, not the task.
func TestHelperNative_ProviderRefusalRerunsPromptOnly(t *testing.T) {
	dir := t.TempDir()
	modes := filepath.Join(dir, "modes")
	answer := mustJSONLine(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{{"type": "text", "text": `{"schema_version":1,"kind":"result","verdict":"pass","summary":"looks fine"}`}}}})
	script := "#!/bin/sh\nfor arg do\n  if [ \"$arg\" = --json-schema ]; then\n    echo native >> " + modes + "\n" +
		"    echo '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":true,\"terminal_reason\":\"api_error\",\"api_error_status\":400,\"result\":\"API Error: 400 schema rejected\"}'\n    exit 1\n  fi\ndone\n" +
		"echo plain >> " + modes + "\ncat <<'EOF'\n" + answer + "\nEOF\necho '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	o, store := newNativeHelperTestOrchestrator(t)
	o.runHelperAgent = o.execHelperAgent

	taskID := attachNativeProfile(t, o, "op-refused", "reviewer")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 10*time.Second)
	o.concurrency.WaitAgents()

	if got := readFileString(t, modes); got != "native\nplain\n" {
		t.Fatalf("runs = %q, want a native run and one prompt-only rerun", got)
	}
	got := readProvenance(t, filepath.Join(o.taskDir(taskID), "turn-1-output.recovery.json"))
	if got.Source != sourcePrompt || got.NativeFallback != nativeFallbackRejected {
		t.Fatalf("provenance = %+v, want a prompt answer after the refusal", got)
	}
}

// fakeNativeHelperClaude puts a claude on PATH that runs nativeRun when asked for native mode and promptRun
// otherwise, logging each run's mode; both are shell snippets.
func fakeNativeHelperClaude(t *testing.T, nativeRun, promptRun string) (modes string) {
	t.Helper()
	dir := t.TempDir()
	modes = filepath.Join(dir, "modes")
	script := "#!/bin/sh\nfor arg do\n  if [ \"$arg\" = --json-schema ]; then\n    echo native >> " + modes + "\n" + nativeRun + "\n    exit $?\n  fi\ndone\n" +
		"echo plain >> " + modes + "\n" + promptRun + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return modes
}

// claudeEnforcedHelperStream is the claude CLI stream of a native turn that ended in ownText (in no text when it is
// empty): the CLI injects its enforcement message and the model then calls StructuredOutput with payload.
func claudeEnforcedHelperStream(t *testing.T, ownText, payload string) string {
	t.Helper()
	stream := ""
	if ownText != "" {
		stream = claudeTextLine(t, ownText) + "\n"
	}
	return stream + `echo '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[structured-output-enforce] You MUST call the StructuredOutput tool to complete this request. Call this tool now."}]},"isSynthetic":true}'
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"StructuredOutput","input":` + payload + `}]}}'
echo '{"type":"result","subtype":"success","is_error":false,"structured_output":` + payload + `}'`
}

// claudeTextLine prints one assistant event with text.
func claudeTextLine(t *testing.T, text string) string {
	t.Helper()
	event := mustJSONLine(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}})
	return "cat <<'EOF'\n" + event + "\nEOF"
}

func claudeTextAnswer(t *testing.T, text string) string {
	t.Helper()
	return claudeTextLine(t, text) + "\necho '{\"type\":\"result\",\"subtype\":\"success\"}'"
}

// A payload the CLI forced into a helper turn reruns it prompt-only once when it could close a required gate the
// model did not close itself: a forced pass over a turn that ended without the model's own pass, or a forced answer
// that does not decode. A forced pass that repeats the model's own pass, or a forced non-pass, stands.
func TestHelperNative_EnforcedAnswerRule(t *testing.T) {
	const (
		forcedApproval = `{"schema_version":1,"kind":"result","verdict":"pass","summary":"forced"}`
		undecodable    = `{"schema_version":1,"kind":"result","verdict":"pass"}`
		forcedIssue    = `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"forced issue","findings":[{"id":"f1","severity":"major","detail":"forced bug"}]}`
		plainIssue     = `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"plain issue","findings":[{"id":"f1","severity":"major","detail":"plain bug"}]}`
		// ownApproval is the text a live helper on qwen3.8-max ended its turn with before the CLI forced the payload.
		ownApproval = `{"schema_version":1,"kind":"result","verdict":"pass","summary":"2+3 = 5","findings":null,"evidence":["Replied to the requesting colleague via afm agent reply."]}`
		ownIssue    = `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"own issue","findings":[{"id":"f1","severity":"major","detail":"own bug"}]}`
		invalidOwn  = `{"schema_version":1,"kind":"result","verdict":"pass","summary":"own","findings":[{"id":"f1","severity":"blocker","detail":"own bug"}]}`
		truncated   = `{"schema_version":1,"kind":"result","verdict":"pass","summary":"2+3`
	)
	type outcome struct{ modes, summary, source, fallback string }
	rerun := outcome{"native\nplain\n", "plain issue", sourcePrompt, nativeFallbackEnforced}
	stands := func(summary string) outcome { return outcome{"native\n", summary, sourceNative, ""} }
	for _, tc := range []struct {
		name, ownText, payload string
		want                   outcome
	}{
		{"forced pass over prose reruns", "done reviewing", forcedApproval, rerun},
		{"forced pass over no text reruns", "", forcedApproval, rerun},
		{"forced pass over the model's own non-pass reruns", ownIssue, forcedApproval, rerun},
		{"forced pass over an invalid own pass reruns", invalidOwn, forcedApproval, rerun},
		{"forced pass over a truncated own pass reruns", truncated, forcedApproval, rerun},
		{"undecodable forced answer reruns", "done reviewing", undecodable, rerun},
		{"undecodable forced answer reruns over the model's own pass", ownApproval, undecodable, rerun},
		{"forced pass repeating the model's own pass stands", ownApproval, forcedApproval, stands("forced")},
		{"forced non-pass stands", "done reviewing", forcedIssue, stands("forced issue")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			modes := fakeNativeHelperClaude(t, claudeEnforcedHelperStream(t, tc.ownText, tc.payload), claudeTextAnswer(t, plainIssue))
			o, store := newNativeHelperTestOrchestrator(t)
			o.runHelperAgent = o.execHelperAgent

			taskID := attachNativeProfile(t, o, "op-enforced", "reviewer")
			waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 10*time.Second)
			o.concurrency.WaitAgents()

			if got := readFileString(t, modes); got != tc.want.modes {
				t.Fatalf("runs = %q, want %q", got, tc.want.modes)
			}
			if committed := readFileString(t, filepath.Join(o.taskDir(taskID), "result.json")); !strings.Contains(committed, tc.want.summary) {
				t.Fatalf("committed result = %s, want %q", committed, tc.want.summary)
			}
			got := readProvenance(t, filepath.Join(o.taskDir(taskID), "turn-1-output.recovery.json"))
			if got.Source != tc.want.source || got.NativeFallback != tc.want.fallback || !got.NativeEnforced {
				t.Fatalf("provenance = %+v, want source %s, fallback %q and the forced answer noted", got, tc.want.source, tc.want.fallback)
			}
			nativeRaw := filepath.Join(o.taskDir(taskID), "result.json.native.raw")
			if tc.want.fallback == "" {
				if _, err := os.Stat(nativeRaw); !os.IsNotExist(err) {
					t.Fatalf("native evidence set aside for an answer that stands: %v", err)
				}
			} else if !strings.Contains(readFileString(t, nativeRaw), `"verdict":"pass"`) {
				t.Fatal("the forced answer is not kept as native evidence")
			}
		})
	}
}

// A turn that yielded gave no answer, so a payload the CLI forced into it never reruns the turn, whatever the model
// wrote before: the yield is handled as such.
func TestHelperNativeRerun_YieldedTurnNeverReruns(t *testing.T) {
	const forcedApproval = `{"schema_version":1,"kind":"result","verdict":"pass","summary":"forced"}`
	for _, ownText := range []string{"", "done reviewing", `{"schema_version":1,"kind":"result","verdict":"pass","summary":"own"}`} {
		outcome := executor.HelperOutcome{ProcessOK: true, NativeStarted: true, NativeEnforced: true, StructuredPayload: true, RawText: forcedApproval, TextBeforeEnforcement: ownText}
		if got := helperNativeRerun(outcome, func(string) bool { return true }, schema.AssistantResultOnly); got != "" {
			t.Fatalf("own text %q: rerun reason %q, want none for a yielded turn", ownText, got)
		}
	}
}

// Setting the native answer aside fails closed, as for verify: no rerun may overwrite evidence it could not keep.
func TestHelperNative_UnkeptNativeEvidenceFailsTheTurn(t *testing.T) {
	modes := fakeNativeHelperClaude(t, claudeEnforcedHelperStream(t, "done reviewing", `{"schema_version":1,"kind":"result","verdict":"pass","summary":"forced"}`), "exit 1")
	o, store := newNativeHelperTestOrchestrator(t)
	o.runHelperAgent = o.execHelperAgent
	taskID := deriveTaskID("op-unkept")
	blocker := filepath.Join(o.taskDir(taskID), "result.json.native.raw")
	if err := os.MkdirAll(filepath.Join(blocker, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}

	attachNativeProfile(t, o, "op-unkept", "reviewer")
	tk := waitForTaskStatus(t, store, taskID, assistants.StatusFailed, 10*time.Second)
	o.concurrency.WaitAgents()

	if !strings.Contains(tk.StatusReason, "keep native run evidence") {
		t.Fatalf("reason = %q, want the evidence failure", tk.StatusReason)
	}
	if got := readFileString(t, modes); got != "native\n" {
		t.Fatalf("runs = %q, want no rerun", got)
	}
}
