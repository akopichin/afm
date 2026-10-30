package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
)

func runStopHookCmd(t *testing.T, stdin string) (stdout, stderr string, err error) {
	t.Helper()
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetArgs([]string{"stop-hook"})
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&errOut)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// The hook command is internal: registered on the root, absent from help.
func TestStopHookCmd_IsHiddenAndRegistered(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"stop-hook"})
	if err != nil || cmd.Name() != "stop-hook" {
		t.Fatalf("Find = %v, %v, want the stop-hook command", cmd, err)
	}
	if !cmd.Hidden {
		t.Error("stop-hook must be hidden")
	}
}

// Whatever is wrong, the CLI gets exactly one JSON object on stdout and a zero exit: a failing hook must never
// block the CLI's stop, and the cause goes to stderr.
func TestStopHookCmd_FailureAnswersAllowOnStdoutAndReportsOnStderr(t *testing.T) {
	tests := []struct {
		name       string
		descriptor string
	}{
		{"no descriptor", ""},
		{"descriptor file missing", filepath.Join(t.TempDir(), "absent.json")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(executor.StopHookDescriptorEnv, tc.descriptor)

			stdout, stderr, err := runStopHookCmd(t, `{"hook_event_name":"Stop"}`)

			if err != nil {
				t.Fatalf("exit error = %v, want exit 0", err)
			}
			var got map[string]any
			if jerr := json.Unmarshal([]byte(stdout), &got); jerr != nil || len(got) != 0 {
				t.Fatalf("stdout = %q, want the empty JSON object", stdout)
			}
			if !strings.Contains(stderr, "stop-hook") {
				t.Fatalf("stderr = %q, want the cause", stderr)
			}
		})
	}
}
