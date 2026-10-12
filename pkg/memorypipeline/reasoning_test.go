package memorypipeline_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/memorypipeline"
)

// Memory-pipeline agents are default-claude invocations, so AgentConfig.Reasoning must reach the
// executor.Config NewExecRunner builds (surfacing as claude --effort).
func TestNewExecRunner_ForwardsReasoning(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	script := "#!/usr/bin/env bash\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + argsFile + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	run := memorypipeline.NewExecRunner(
		memorypipeline.AgentConfig{Command: "claude", RootDir: dir, Reasoning: "high"},
		memorypipeline.Prompts{Reflect: "reflect"},
	)
	spec := memorypipeline.AgentSpec{Kind: memorypipeline.KindReflect, LogFile: filepath.Join(dir, "reflect.log")}
	if err := run(context.Background(), spec); err != nil {
		t.Fatalf("run: %v", err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if !strings.Contains(string(args), "--effort\nhigh") {
		t.Errorf("expected --effort high in memory agent args:\n%s", args)
	}
}
