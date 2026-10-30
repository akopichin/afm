package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/browser"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
)

func newOrchWithBrowser(t *testing.T, d *browser.Descriptor) *Orchestrator {
	t.Helper()
	return &Orchestrator{
		opts: Options{RunDir: t.TempDir(), Config: config.Config{}, BrowserDescriptor: d},
		ui:   bus.NewUIBus(),
	}
}

func normalClaudeStage(id string) flow.Stage {
	return flow.Stage{ID: id, Command: "claude", Agents: []flow.AgentType{flow.AgentImplementation}}
}

func stageWithBrowserFalse(id string) flow.Stage {
	fa := false
	s := normalClaudeStage(id)
	s.Browser = &fa
	return s
}

func interactiveClaudeStage(id string) flow.Stage {
	s := normalClaudeStage(id)
	s.Interactive = true
	return s
}

func execConfigHasBrowser(r executor.Runner) bool {
	e, ok := r.(*executor.Executor)
	return ok && e.BrowserMCPConfig() != nil
}

func TestBrowserForStage_Policy(t *testing.T) {
	o := newOrchWithBrowser(t, &browser.Descriptor{Endpoint: "127.0.0.1:1", Epoch: "ep1"})
	if o.browserForStage(normalClaudeStage("build"), "claude") == nil {
		t.Fatal("normal supported stage must get the capability")
	}
	if o.browserForStage(stageWithBrowserFalse("build"), "claude") != nil {
		t.Fatal("browser:false stage must get nil")
	}
	if o.browserForStage(normalClaudeStage("build"), "deepseek-openai") != nil {
		t.Fatal("unsupported client (no MCP tool-loop) must get nil")
	}
}

func TestBrowserForStage_NilDescriptor(t *testing.T) {
	o := newOrchWithBrowser(t, nil)
	if o.browserForStage(normalClaudeStage("build"), "claude") != nil {
		t.Fatal("no descriptor → capability off")
	}
}

func TestRunnerFor_InteractiveAndFallbackGetBrowser(t *testing.T) {
	o := newOrchWithBrowser(t, &browser.Descriptor{Endpoint: "127.0.0.1:1", Epoch: "ep1"})
	if !execConfigHasBrowser(o.runnerFor(interactiveClaudeStage("build"), "implementation")) {
		t.Fatal("interactive author runner should receive afm_chrome")
	}
	if !execConfigHasBrowser(o.runnerForFallback(interactiveClaudeStage("build"), "implementation", nil)) {
		t.Fatal("interactive fallback runner must also receive afm_chrome")
	}
}

func TestRunnerFor_NonInteractiveGetsBrowser(t *testing.T) {
	o := newOrchWithBrowser(t, &browser.Descriptor{Endpoint: "127.0.0.1:1", Epoch: "ep1"})
	if !execConfigHasBrowser(o.runnerFor(normalClaudeStage("build"), "implementation")) {
		t.Fatal("non-interactive author runner should receive afm_chrome")
	}
}

func TestRunnerForVerify_NoBrowser(t *testing.T) {
	o := newOrchWithBrowser(t, &browser.Descriptor{Endpoint: "127.0.0.1:1", Epoch: "ep1"})
	if execConfigHasBrowser(o.runnerForVerify(context.Background(), normalClaudeStage("build"), "codex-as-claude")) {
		t.Fatal("verify runner must never receive the browser capability")
	}
}

func TestBrowserForStage_CodexFlag(t *testing.T) {
	o := &Orchestrator{
		opts: Options{
			RunDir:            t.TempDir(),
			BrowserDescriptor: &browser.Descriptor{Endpoint: "127.0.0.1:1", Epoch: "ep1"},
			Config: config.Config{Docker: config.DockerConfig{Agents: map[string]config.AgentRecipe{
				"codexrec": {Type: config.RecipeTypeCodex},
			}}},
		},
		ui: bus.NewUIBus(),
	}
	b := o.browserForStage(flow.Stage{ID: "build", Command: "codexrec"}, "codexrec")
	if b == nil || !b.Codex {
		t.Fatalf("codex recipe must set Codex=true, got %+v", b)
	}
	cl := o.browserForStage(normalClaudeStage("build"), "claude")
	if cl == nil || cl.Codex {
		t.Fatalf("claude must set Codex=false, got %+v", cl)
	}
}

// afm-root != flow.root_dir (the Docker layout): the descriptor roots must carry BOTH the project root and
// the stage dir so the MCP can read the project and write artifacts.
func TestBrowserForStage_RootsCoverProjectAndStageDir(t *testing.T) {
	runDir := t.TempDir()
	o := &Orchestrator{
		opts: Options{
			RunDir: runDir, RootDir: "/workspace", Config: config.Config{},
			BrowserDescriptor: &browser.Descriptor{Endpoint: "127.0.0.1:1", Epoch: "ep1"},
		},
		ui: bus.NewUIBus(),
	}
	b := o.browserForStage(flow.Stage{ID: "build", Command: "claude"}, "claude")
	if b == nil {
		t.Fatal("expected capability")
	}
	wantStage := filepath.Join(runDir, "build")
	if len(b.Roots) != 2 || b.Roots[0] != "/workspace" || b.Roots[1] != wantStage {
		t.Fatalf("roots must be [RootDir, stageDir], got %v", b.Roots)
	}
}

func TestBrowserForStage_EmptyRootDirUsesCWD(t *testing.T) {
	runDir := t.TempDir()
	o := newOrchWithBrowser(t, &browser.Descriptor{Endpoint: "127.0.0.1:1", Epoch: "ep1"})
	o.opts.RunDir = runDir // RootDir left "" → the agent inherits afm's CWD as root
	b := o.browserForStage(flow.Stage{ID: "build", Command: "claude"}, "claude")
	if b == nil {
		t.Fatal("expected capability")
	}
	wd, _ := os.Getwd()
	wantStage := filepath.Join(runDir, "build")
	// An empty project root resolves to the CWD (the agent's project root), and
	// every root is absolute so the MCP resolves them correctly under the agent CWD.
	if len(b.Roots) != 2 || b.Roots[0] != wd || b.Roots[1] != wantStage {
		t.Fatalf("empty RootDir must yield [cwd, stageDir], got %v", b.Roots)
	}
	for _, r := range b.Roots {
		if !filepath.IsAbs(r) {
			t.Fatalf("every filesystemRoot must be absolute, got %q", r)
		}
	}
}

// The shell-verify env path (sanitizedShellVerifyEnv) is a security-relevant
// default-deny site that must also strip the browser transport.
func TestSanitizedShellVerifyEnv_StripsBrowserTransport(t *testing.T) {
	t.Setenv("AFM_BROWSER_TRANSPORT_TOKEN", "x")
	t.Setenv("AFM_BROWSER_ENDPOINT", "y")
	for _, kv := range sanitizedShellVerifyEnv() {
		if strings.HasPrefix(kv, "AFM_BROWSER_") {
			t.Fatalf("shell verify env must strip AFM_BROWSER_*: %q", kv)
		}
	}
}
