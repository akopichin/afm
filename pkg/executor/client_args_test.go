package executor_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
)

func TestClaudeExtraArgsKeepProtocol(t *testing.T) {
	for _, command := range []string{"", "claude"} {
		t.Run("command="+command, func(t *testing.T) {
			argsFile := stubClaudeRecorder(t)
			extra := []string{"--model", "sonnet", "--fallback-model", "sonnet"}
			got := runClaudeAndReadArgs(t, executor.Config{Command: command, ExtraArgs: extra}, argsFile)
			want := append(executor.DefaultClaudeArgs(), extra...)
			if args := strings.Split(strings.TrimSuffix(got, "\n"), "\n"); !slices.Equal(args, want) {
				t.Fatalf("Claude argv = %q, want %q", args, want)
			}
		})
	}
}

func TestResolveArgsPreservesRepeatedValues(t *testing.T) {
	for _, extra := range [][]string{
		{"--model", "sonnet", "--fallback-model", "sonnet"},
		{"--add-dir", "one", "--add-dir", "two"},
		{"--system-prompt", "--verbose"},
	} {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			want := append(executor.DefaultClaudeArgs(), extra...)
			if got := executor.ResolveArgs(extra); !slices.Equal(got, want) {
				t.Fatalf("argv = %q, want %q", got, want)
			}
		})
	}
}

func TestResolveArgsAlreadyNormalized(t *testing.T) {
	extra := append(executor.DefaultClaudeArgs(), "--model", "sonnet", "--fallback-model", "sonnet")
	got := executor.ResolveArgs(extra)
	if !slices.Equal(got, extra) {
		t.Fatalf("argv = %q, want %q", got, extra)
	}
	got[0] = "changed"
	if extra[0] != "--print" {
		t.Fatal("resolved argv aliases the caller's slice")
	}
}
