package memorypipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeAgentScript writes a small hermetic shell script that:
//  1. copies its stdin (the built prompt) into "prompt.out" next to itself,
//  2. emits one well-formed claude stream-json assistant line (so the
//     executor's line parser and isErrorLine check see valid output, not an
//     "error"-looking string), and
//  3. exits 0.
//
// This drives the REAL NewExecRunner -> executor.New -> RunAgent path end
// to end (no stub), proving the AgentConfig -> executor.Config wiring and
// prompt-via-stdin delivery actually work — NewExecRunner had zero test
// coverage before (every other test in this package injects a stub
// AgentRunner via WithRunner).
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

// TestNewExecRunner_InvokesCommandAndDeliversPrompt drives the real
// production AgentRunner (NewExecRunner) against a tiny fake command
// instead of a real agent binary: it asserts the command actually ran (its
// side-effect file was written) and that the built prompt (source paths +
// dataset-out instructions, see BuildPrompt) reached the process via stdin,
// exactly as executor.RunAgent wires it.
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

// TestNewExecRunner_CommandNotFoundSurfacesError proves a missing command
// surfaces as a real error rather than a silent success — the minimal bar
// if the real-invocation test above ever proves too environment-sensitive.
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
