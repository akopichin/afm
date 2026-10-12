package memorypipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
)

// writeFakeAgentScript captures stdin in prompt.out and emits a successful Claude text result.
func writeFakeAgentScript(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-agent.sh")
	body := "#!/bin/sh\n" +
		`dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)` + "\n" +
		`cat > "$dir/prompt.out"` + "\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}'` + "\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestNewExecRunner_InvokesCommandAndDeliversPrompt(t *testing.T) {
	script := writeFakeAgentScript(t)
	scriptDir := filepath.Dir(script)
	rootDir := t.TempDir()
	logDir := t.TempDir()

	cfg := AgentConfig{
		Command: script,
		RootDir: rootDir,
	}
	prompts := Prompts{Reflect: "REFLECT BASE PROMPT"}
	run := NewExecRunner(cfg, prompts)

	sourcePath := filepath.Join(rootDir, "s1", "implementation.log")
	datasetOut := filepath.Join(rootDir, "s1", "reflect_dataset.yaml")
	spec := AgentSpec{
		Kind:       KindReflect,
		StageName:  "s1",
		Sources:    []string{sourcePath},
		DatasetOut: datasetOut,
		LogFile:    filepath.Join(logDir, "reflect.log"),
	}

	if err := run(context.Background(), spec); err != nil {
		t.Fatalf("NewExecRunner-built runner returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(scriptDir, "prompt.out"))
	if err != nil {
		t.Fatalf("fake agent script did not receive stdin (command was not actually invoked): %v", err)
	}
	prompt := string(got)
	if !strings.Contains(prompt, "REFLECT BASE PROMPT") {
		t.Errorf("prompt missing the base reflect template:\n%s", prompt)
	}
	if !strings.Contains(prompt, sourcePath) {
		t.Errorf("prompt missing the source path %q:\n%s", sourcePath, prompt)
	}
	if !strings.Contains(prompt, datasetOut) {
		t.Errorf("prompt missing the dataset-out path %q:\n%s", datasetOut, prompt)
	}

	if _, err := os.Stat(spec.LogFile); err != nil {
		t.Errorf("expected RunAgent to write the log file at %q: %v", spec.LogFile, err)
	}
}

func TestNewExecRunner_CommandNotFoundSurfacesError(t *testing.T) {
	rootDir := t.TempDir()
	logDir := t.TempDir()

	cfg := AgentConfig{
		Command: filepath.Join(rootDir, "does-not-exist"),
		RootDir: rootDir,
	}
	run := NewExecRunner(cfg, Prompts{Reflect: "x"})
	if run == nil {
		t.Fatal("NewExecRunner returned a nil AgentRunner")
	}

	spec := AgentSpec{
		Kind:       KindReflect,
		StageName:  "s1",
		Sources:    []string{"/tmp/whatever"},
		DatasetOut: filepath.Join(rootDir, "reflect_dataset.yaml"),
		LogFile:    filepath.Join(logDir, "reflect.log"),
	}
	if err := run(context.Background(), spec); err == nil {
		t.Fatal("expected an error for a nonexistent command, got nil")
	}
}

// writeUsageAgentScript emits a metered Claude result without real agents or distillation file effects.
func writeUsageAgentScript(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "usage-agent.sh")
	body := "#!/bin/sh\n" +
		"cat > /dev/null\n" +
		`echo '{"type":"result","subtype":"success","usage":{"input_tokens":5,"output_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}'` + "\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestNewExecRunner_UsageAttribution(t *testing.T) {
	script := writeUsageAgentScript(t)
	rootDir := t.TempDir()
	logDir := t.TempDir()

	type recordedCall struct {
		stageID, phase, scope string
		metered               bool
	}
	var calls []recordedCall

	cfg := AgentConfig{
		Command: script,
		RootDir: rootDir,
		OnUsage: func(stageID, phase, scope string) func(accounting.Observation) {
			return func(obs accounting.Observation) {
				calls = append(calls, recordedCall{stageID: stageID, phase: phase, scope: scope, metered: obs.Metered})
			}
		},
	}
	run := NewExecRunner(cfg, Prompts{})

	specs := []AgentSpec{
		{
			Kind: KindReflect, StageName: "s1", StageID: "s1", Phase: PhaseReflect,
			Sources:    []string{filepath.Join(rootDir, "s1", "implementation.log")},
			DatasetOut: filepath.Join(rootDir, "s1", "reflect_dataset.yaml"),
			LogFile:    filepath.Join(logDir, "reflect.log"),
		},
		{
			Kind: KindAggregate, StageName: "flow-memory", Phase: PhaseAggregate, Scope: ScopeRunOverhead,
			InPaths: []string{filepath.Join(rootDir, "s1", "reflect_dataset.yaml")},
			Out:     filepath.Join(rootDir, "patterns.md"),
			LogFile: filepath.Join(logDir, "aggregate.log"),
		},
		{
			Kind: KindPrioritize, StageName: "flow-memory", Phase: PhasePrioritize, Scope: ScopeRunOverhead,
			In:      filepath.Join(rootDir, "patterns.md"),
			Out:     filepath.Join(rootDir, "prioritized.md"),
			LogFile: filepath.Join(logDir, "prioritize.log"),
		},
		{
			Kind: KindUpdate, StageName: "flow-memory", Phase: PhaseUpdate, Scope: ScopeRunOverhead,
			HighPath:   filepath.Join(rootDir, "high.md"),
			TargetFile: filepath.Join(rootDir, "memory.md"),
			LogFile:    filepath.Join(logDir, "update.log"),
		},
	}

	for i, spec := range specs {
		if err := run(context.Background(), spec); err != nil {
			t.Fatalf("spec[%d] kind=%s: run: %v", i, spec.Kind, err)
		}
	}

	want := []recordedCall{
		{stageID: "s1", phase: PhaseReflect, scope: "", metered: true},
		{stageID: "", phase: PhaseAggregate, scope: ScopeRunOverhead, metered: true},
		{stageID: "", phase: PhasePrioritize, scope: ScopeRunOverhead, metered: true},
		{stageID: "", phase: PhaseUpdate, scope: ScopeRunOverhead, metered: true},
	}
	if len(calls) != len(want) {
		t.Fatalf("OnUsage fired %d times, want %d: %+v", len(calls), len(want), calls)
	}
	for i, w := range want {
		if calls[i] != w {
			t.Errorf("call[%d] = %+v, want %+v", i, calls[i], w)
		}
	}
}
