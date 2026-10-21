package docker

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCreateWrappers_ClaudeModelSelection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typ   string
		model string
		args  []string
		want  string
	}{
		{
			name:  "explicit Claude recipe",
			typ:   "claude",
			model: "claude-opus-4-6",
			args:  []string{"-p", "prompt with spaces"},
			want:  "claude-opus-4-6",
		},
		{
			name:  "GLM recipe",
			model: "glm-5.3",
			want:  "glm-5.3",
		},
		{
			name:  "GLM context suffix is passed literally",
			model: "glm-5.2[1m]",
			want:  "glm-5.2[1m]",
		},
		{
			name:  "DeepSeek recipe",
			model: "deepseek-v4-pro",
			want:  "deepseek-v4-pro",
		},
		{
			name:  "default Claude recipe overrides client args on resume",
			model: "claude-sonnet-4-6",
			args:  []string{"--resume", "previous-session", "--model", "claude-fable-5"},
			want:  "claude-sonnet-4-6",
		},
		{
			name:  "custom model is passed literally",
			model: "provider's custom model",
			args:  []string{"--model=claude-fable-5"},
			want:  "provider's custom model",
		},
		{
			name: "proxy without a model preserves client selection",
			args: []string{"--model", "caller-model"},
			want: "caller-model",
		},
		{
			name: "proxy without a model preserves defaults",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			realClaude := stubClaudeOnPATH(t)
			// Capture the actual argv crossing the shell boundary, without calling a model.
			if err := os.WriteFile(realClaude, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\"\n"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ANTHROPIC_MODEL", "claude-fable-5")
			dir, err := CreateWrappers([]WrapperSpec{{Type: tc.typ, Command: "recipe", Model: tc.model}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cleanup(dir) })
			out, err := exec.Command(filepath.Join(dir, "recipe"), tc.args...).CombinedOutput()
			if err != nil {
				t.Fatalf("run wrapper: %v\n%s", err, out)
			}
			args := strings.Split(string(bytes.TrimSuffix(out, []byte{0})), "\x00")
			if len(args) < len(tc.args) || !slices.Equal(args[:len(tc.args)], tc.args) {
				t.Fatalf("caller arguments changed: got %q, want prefix %q", args, tc.args)
			}
			// Claude's string option uses the last --model, including on --resume.
			var model string
			for i, arg := range args {
				if arg == "--model" && i+1 < len(args) {
					model = args[i+1]
				} else if value, ok := strings.CutPrefix(arg, "--model="); ok {
					model = value
				}
			}
			if model != tc.want {
				t.Errorf("model argument = %q, want %q; argv: %q", model, tc.want, args)
			}
		})
	}
}
