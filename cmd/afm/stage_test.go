package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/mcp"
)

type stageRun struct {
	stdout, stderr string
	code           int
}

// runStage executes `afm stage <args>` through the root command with stdin and the author environment,
// returning the exit code the process would have.
func runStage(t *testing.T, stdin string, args ...string) stageRun {
	t.Helper()
	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"stage"}, args...))
	err := cmd.Execute()
	res := stageRun{stdout: out.String(), stderr: errOut.String()}
	var exitErr *ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.code = exitErr.Code
		if !exitErr.Silent {
			t.Fatalf("stage commands must be silent on failure, got %v", err)
		}
	default:
		t.Fatalf("unexpected error type: %v", err)
	}
	return res
}

func setStageAuthorEnv(t *testing.T, phase string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AFM_STAGE_DIR", dir)
	t.Setenv("AFM_DIALOG_PHASE", phase)
	t.Setenv("AFM_WAIT_ANSWER_TIMEOUT", "")
	return dir
}

func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not one JSON object: %q: %v", s, err)
	}
	return m
}

func TestStageAsk_CreatesQuestionAndPrintsWaitCommand(t *testing.T) {
	dir := setStageAuthorEnv(t, "planning")
	res := runStage(t, "## Context\n\nWhich database?\n", "ask", "--option", "pg", "--option", "sqlite")
	if res.code != 0 || res.stderr != "" {
		t.Fatalf("%+v", res)
	}
	out := decodeJSON(t, res.stdout)
	if out["id"] != "q1" || !strings.Contains(out["wait"].(string), "afm stage wait-answer q1") {
		t.Fatalf("stdout = %v", out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "planning.q1.question.json"))
	if err != nil {
		t.Fatal(err)
	}
	q, fixed, _, err := mcp.RecoverQuestion(raw)
	if err != nil || !bytes.Equal(raw, fixed) {
		t.Fatalf("question file is not accepted as is: %v", err)
	}
	if q.Question != "## Context\n\nWhich database?" || len(q.Options) != 2 || q.AllowCustom == nil || !*q.AllowCustom {
		t.Fatalf("question = %+v", q)
	}
}

func TestStageAsk_AllowCustomFalse(t *testing.T) {
	dir := setStageAuthorEnv(t, "implementation")
	res := runStage(t, "Pick one", "ask", "--option", "a", "--allow-custom=false")
	if res.code != 0 {
		t.Fatalf("%+v", res)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "implementation.q1.question.json"))
	if !strings.Contains(string(raw), `"allow_custom":false`) {
		t.Fatalf("file = %s", raw)
	}
}

func TestStageAsk_RepeatIsIdempotentAndOtherTextIsPending(t *testing.T) {
	setStageAuthorEnv(t, "planning")
	runStage(t, "Same?", "ask")
	again := runStage(t, "Same?\n", "ask")
	if again.code != 0 || decodeJSON(t, again.stdout)["id"] != "q1" {
		t.Fatalf("repeat = %+v", again)
	}
	other := runStage(t, "Different?", "ask")
	if other.code != 1 || other.stdout != "" {
		t.Fatalf("other = %+v", other)
	}
	e := decodeJSON(t, other.stderr)
	if e["error"] != "question_pending" || !strings.Contains(e["message"].(string), "q1") {
		t.Fatalf("stderr = %v", e)
	}
}

func TestStageAsk_ValidationErrorsAreJSONOnStderr(t *testing.T) {
	setStageAuthorEnv(t, "planning")
	for name, tc := range map[string]struct {
		stdin string
		args  []string
		field string
	}{
		"empty question": {"  \n", nil, "question"},
		"empty option":   {"q?", []string{"--option", ""}, "options[0]"},
		"unanswerable":   {"q?", []string{"--allow-custom=false"}, "allow_custom"},
	} {
		t.Run(name, func(t *testing.T) {
			res := runStage(t, tc.stdin, append([]string{"ask"}, tc.args...)...)
			if res.code != 1 || res.stdout != "" {
				t.Fatalf("%+v", res)
			}
			e := decodeJSON(t, res.stderr)
			if e["error"] != "invalid_question" || !strings.Contains(e["message"].(string), tc.field) {
				t.Fatalf("stderr = %v", e)
			}
		})
	}
}

// A wrong call (a bad flag value, an unknown flag, an extra argument) reaches the agent as the same single JSON
// error as any other mistake, never as cobra's usage text.
func TestStageCommands_CallMistakesAreOneJSONError(t *testing.T) {
	setStageAuthorEnv(t, "planning")
	for name, args := range map[string][]string{
		"bad bool flag":        {"ask", "--allow-custom=maybe"},
		"unknown flag":         {"ask", "--bogus"},
		"argument to ask":      {"ask", "extra"},
		"bad timeout":          {"wait-answer", "q1", "--timeout", "soon"},
		"extra wait arguments": {"wait-answer", "q1", "q2"},
	} {
		t.Run(name, func(t *testing.T) {
			res := runStage(t, "q?", args...)
			if res.code != 1 || res.stdout != "" {
				t.Fatalf("%+v, want exit 1 and nothing on stdout", res)
			}
			if e := decodeJSON(t, res.stderr); e["error"] != "invalid_arguments" || e["message"] == "" {
				t.Fatalf("stderr = %v", e)
			}
		})
	}
}

// The exit codes the agent relies on survive the real entry point.
func TestStageCommands_ExitCodesThroughRunMain(t *testing.T) {
	setStageAuthorEnv(t, "planning")
	if res := runStage(t, "q?", "ask"); res.code != 0 {
		t.Fatalf("ask: %+v", res)
	}
	var code int
	_, stderr := captureOutput(t, func() { code = runMain([]string{"stage", "ask", "--allow-custom=maybe"}) })
	if code != 1 {
		t.Fatalf("bad flag exit = %d, want 1", code)
	}
	if e := decodeJSON(t, stderr); e["error"] != "invalid_arguments" {
		t.Fatalf("bad flag stderr = %v", e)
	}
	stdout, _ := captureOutput(t, func() { code = runMain([]string{"stage", "wait-answer", "q1", "--timeout", "1"}) })
	if code != stageExitWaiting || decodeJSON(t, stdout)["status"] != "waiting" {
		t.Fatalf("wait exit = %d stdout = %q, want %d and the waiting status", code, stdout, stageExitWaiting)
	}
}

func TestStageCommands_NotStageAuthor(t *testing.T) {
	absent := func(t *testing.T) { t.Setenv("AFM_STAGE_DIR", ""); t.Setenv("AFM_DIALOG_PHASE", "") }
	badPhase := func(t *testing.T) { setStageAuthorEnv(t, "verify") }
	relDir := func(t *testing.T) {
		t.Setenv("AFM_STAGE_DIR", "relative/dir")
		t.Setenv("AFM_DIALOG_PHASE", "planning")
	}
	noPhase := func(t *testing.T) { setStageAuthorEnv(t, "") }
	for name, setup := range map[string]func(*testing.T){"no env": absent, "bad phase": badPhase, "relative dir": relDir, "no phase": noPhase} {
		for _, args := range [][]string{{"ask"}, {"wait-answer", "q1"}} {
			t.Run(name+"/"+args[0], func(t *testing.T) {
				setup(t)
				res := runStage(t, "q?", args...)
				if res.code != 1 || res.stdout != "" {
					t.Fatalf("%+v", res)
				}
				if e := decodeJSON(t, res.stderr); e["error"] != "not_stage_author" {
					t.Fatalf("stderr = %v", e)
				}
			})
		}
	}
}

func TestStageWaitAnswer_ReturnsExistingAnswer(t *testing.T) {
	dir := setStageAuthorEnv(t, "planning")
	runStage(t, "Proceed?", "ask", "--option", "yes")
	if err := mcp.WriteAnswerFile(dir, "planning", "q1", "yes", true); err != nil {
		t.Fatal(err)
	}
	res := runStage(t, "", "wait-answer", "q1", "--timeout", "1")
	if res.code != 0 {
		t.Fatalf("%+v", res)
	}
	out := decodeJSON(t, res.stdout)
	if out["id"] != "q1" || out["answer"] != "yes" || !out["from_options"].(bool) {
		t.Fatalf("stdout = %v", out)
	}
}

func TestStageWaitAnswer_WithoutArgWaitsForLastQuestion(t *testing.T) {
	dir := setStageAuthorEnv(t, "planning")
	runStage(t, "one?", "ask")
	_ = mcp.WriteAnswerFile(dir, "planning", "q1", "a", false)
	runStage(t, "two?", "ask")
	_ = mcp.WriteAnswerFile(dir, "planning", "q2", "b", false)
	res := runStage(t, "", "wait-answer", "--timeout", "1")
	if res.code != 0 || decodeJSON(t, res.stdout)["id"] != "q2" {
		t.Fatalf("%+v", res)
	}
}

// Past q9 the latest question is still the highest number, not the last file name.
func TestStageWaitAnswer_WithoutArgWaitsForQ10(t *testing.T) {
	dir := setStageAuthorEnv(t, "planning")
	for n := 1; n <= 10; n++ {
		id := fmt.Sprintf("q%d", n)
		if res := runStage(t, id+"?", "ask"); res.code != 0 || decodeJSON(t, res.stdout)["id"] != id {
			t.Fatalf("ask %s: %+v", id, res)
		}
		if n < 10 {
			if err := mcp.WriteAnswerFile(dir, "planning", id, "ok", false); err != nil {
				t.Fatal(err)
			}
		}
	}
	res := runStage(t, "", "wait-answer", "--timeout", "1")
	if res.code != stageExitWaiting || decodeJSON(t, res.stdout)["id"] != "q10" {
		t.Fatalf("%+v, want to keep waiting for q10", res)
	}
}

func TestStageWaitAnswer_TimeoutExits75WithRetryHint(t *testing.T) {
	setStageAuthorEnv(t, "planning")
	runStage(t, "q?", "ask")
	restore := stageWaitInterval
	stageWaitInterval = 5 * time.Millisecond
	t.Cleanup(func() { stageWaitInterval = restore })
	// --timeout is in whole seconds; the smallest allowed value keeps the test at one second.
	res := runStage(t, "", "wait-answer", "q1", "--timeout", "1")
	if res.code != 75 || res.stderr != "" {
		t.Fatalf("%+v", res)
	}
	out := decodeJSON(t, res.stdout)
	if out["status"] != "waiting" || out["id"] != "q1" || !strings.Contains(out["retry"].(string), "afm stage wait-answer q1") {
		t.Fatalf("stdout = %v", out)
	}
}

func TestStageWaitAnswer_PartialAnswerKeepsWaiting(t *testing.T) {
	dir := setStageAuthorEnv(t, "planning")
	runStage(t, "q?", "ask")
	if err := os.WriteFile(filepath.Join(dir, "planning.q1.answer.json"), []byte(`{"id":"q1","ans`), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := stageWaitInterval
	stageWaitInterval = 5 * time.Millisecond
	t.Cleanup(func() { stageWaitInterval = restore })
	if res := runStage(t, "", "wait-answer", "q1", "--timeout", "1"); res.code != 75 {
		t.Fatalf("%+v", res)
	}
}

func TestStageWaitAnswer_AnswerArrivingDuringWait(t *testing.T) {
	dir := setStageAuthorEnv(t, "planning")
	runStage(t, "q?", "ask")
	restore := stageWaitInterval
	stageWaitInterval = 5 * time.Millisecond
	t.Cleanup(func() { stageWaitInterval = restore })
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = mcp.WriteAnswerFile(dir, "planning", "q1", "late", false)
	}()
	res := runStage(t, "", "wait-answer", "q1", "--timeout", "30")
	if res.code != 0 || decodeJSON(t, res.stdout)["answer"] != "late" {
		t.Fatalf("%+v", res)
	}
}

func TestStageWaitAnswer_NoQuestion(t *testing.T) {
	setStageAuthorEnv(t, "planning")
	for _, args := range [][]string{{"wait-answer"}, {"wait-answer", "q9"}} {
		res := runStage(t, "", args...)
		if res.code != 1 || res.stdout != "" {
			t.Fatalf("%v: %+v", args, res)
		}
		if e := decodeJSON(t, res.stderr); e["error"] != "no_question" {
			t.Fatalf("%v: stderr = %v", args, e)
		}
	}
}

func TestStageWaitAnswer_InvalidID(t *testing.T) {
	setStageAuthorEnv(t, "planning")
	res := runStage(t, "", "wait-answer", "../q1")
	if res.code != 1 || decodeJSON(t, res.stderr)["error"] != "invalid_question_id" {
		t.Fatalf("%+v", res)
	}
}

func TestWaitAnswerTimeout(t *testing.T) {
	tests := []struct {
		name   string
		env    string
		flag   int
		wanted time.Duration
	}{
		{"default", "", 0, 100 * time.Second},
		{"env", "600", 0, 600 * time.Second},
		{"env clamped high", "9999", 0, 600 * time.Second},
		{"env clamped low", "0", 0, 1 * time.Second},
		{"env garbage", "soon", 0, 100 * time.Second},
		{"flag beats env", "600", 30, 30 * time.Second},
		{"flag clamped", "", 5000, 600 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AFM_WAIT_ANSWER_TIMEOUT", tc.env)
			if got := waitAnswerTimeout(tc.flag); got != tc.wanted {
				t.Fatalf("timeout = %v, want %v", got, tc.wanted)
			}
		})
	}
}
