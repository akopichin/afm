package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
)

// A verifier's raw actions stay out of the stage feed whatever adapter runs it; its own log keeps them.
func TestVerifyExecutorConfig_PublishesNoRawActions(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	stage := flow.Stage{ID: assistantsTestStageID}
	for _, cmd := range []string{"claude", "codex-as-claude", "openai-agent-as-claude", "openai-as-claude", "cursor-agent-as-claude"} {
		cfg := o.verifyExecutorConfig(stage, cmd, 1)
		if cfg.OnAction != nil || cfg.OnActivity != nil {
			t.Errorf("%s: verify config publishes raw actions", cmd)
		}
		if cfg.OnLimitWarning == nil {
			t.Errorf("%s: verify must keep its usage-limit warning", cmd)
		}
	}
}

// fakeClaudeWithAction is a Claude-shaped helper that runs one tool and notes the environment it was started with.
func fakeClaudeWithAction(t *testing.T, marker string) (cmd, envFile string) {
	t.Helper()
	dir := t.TempDir()
	cmd, envFile = filepath.Join(dir, "fake-claude"), filepath.Join(dir, "env")
	script := "#!/bin/sh\ncat >/dev/null\nenv > " + envFile + "\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"c1","name":"Bash","input":{"command":"echo ` + marker + `"}}]}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"{}"}'` + "\n"
	if err := os.WriteFile(cmd, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture: an executable fake agent
		t.Fatal(err)
	}
	return cmd, envFile
}

// assertNothingPublished fails when a subscriber saw a raw action or an activity event.
func assertNothingPublished(t *testing.T, sub <-chan bus.Event) {
	t.Helper()
	for len(sub) > 0 {
		if ev := <-sub; ev.Type == bus.EventAgentAction || ev.Type == bus.EventAssistantActivity {
			t.Errorf("published %+v to the stage feed", ev)
		}
	}
}

func assertNoStreamExtension(t *testing.T, envFile string) {
	t.Helper()
	raw, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), executor.StreamExtensionEnv+"=") {
		t.Errorf("the helper was asked to stream: %s", executor.StreamExtensionEnv)
	}
}

// Both verify paths — the CLI's native schema mode and the prompt-only rerun — keep the action out of the feed.
func TestExecVerifyAgent_NativeAndPromptOnlyShowNoActionInFeed(t *testing.T) {
	const marker = "verify-tool-marker"
	cmd, envFile := fakeClaudeWithAction(t, marker)
	for name, c := range map[string]struct {
		ctx    context.Context
		native bool
	}{
		"native":      {context.Background(), true},
		"prompt-only": {withPromptOnlyVerify(context.Background()), false},
	} {
		t.Run(name, func(t *testing.T) {
			o, _ := newAssistantsTestOrchestrator(t, true)
			// The recipe makes the fake command a claude-family one, so native mode is really requested.
			o.opts.Config.Docker.Agents = map[string]config.AgentRecipe{cmd: {Type: config.ClaudeCommand}}
			_, sub := o.ui.Subscribe(64)
			logFile := filepath.Join(t.TempDir(), "verify.log")
			out, err := o.execVerifyAgent(c.ctx, flow.Stage{ID: assistantsTestStageID, Name: "stage"}, cmd, "verify", logFile, filepath.Join(t.TempDir(), "verify.json"))
			if err != nil {
				t.Fatalf("execVerifyAgent: %v", err)
			}
			if out.NativeStarted != c.native {
				t.Errorf("NativeStarted = %v, want %v: the case must run the mode it names", out.NativeStarted, c.native)
			}
			assertNothingPublished(t, sub)
			assertNoStreamExtension(t, envFile)
			if raw, err := os.ReadFile(logFile); err != nil || !strings.Contains(string(raw), marker) {
				t.Errorf("the verify log lost the action: %v\n%s", err, raw)
			}
		})
	}
}

// The format repair runs as a helper too, but nobody watches it: its tool use stays out of the feed.
func TestOutputFormatterRun_ShowsNoActionInFeed(t *testing.T) {
	cmd, envFile := fakeClaudeWithAction(t, "repair-tool-marker")
	o, _ := newAssistantsTestOrchestrator(t, true)
	_, sub := o.ui.Subscribe(64)
	cfg := o.outputFormatterConfig(cmd, assistantsTestStageID)
	if _, err := executor.New(cfg).RunJSONQuery(context.Background(), "fix the json"); err != nil {
		t.Fatalf("RunJSONQuery: %v", err)
	}
	assertNothingPublished(t, sub)
	assertNoStreamExtension(t, envFile)
}
