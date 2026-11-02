package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

const plainStageID = "plain" // a stage without a command of its own

var (
	clientModelArgs = []string{"--model", "client-model"}
	userActor       = state.Actor{Kind: "user"}
)

// newExecutionTestOrchestrator is the white-box harness with a role-only profile, a profile with its own command,
// and a client whose extra_args carry a model pair among flags a helper must not inherit. Its "impl" stage runs
// claude; extra stages run whatever they name.
func newExecutionTestOrchestrator(t *testing.T, extraStages ...flow.Stage) (*Orchestrator, *state.Store) {
	t.Helper()
	o, store := newAssistantsTestOrchestrator(t, true, extraStages...)
	reg, err := assistants.BuildRegistry(assistants.BuiltinProfiles(), []assistants.Profile{
		{ID: "role", Title: "Role", Instructions: "review", Capabilities: assistants.Capabilities{Delegate: true}},
		{ID: "explicit", Title: "Explicit", Command: "ext-cmd", Capabilities: assistants.Capabilities{Delegate: true}},
		{ID: "codex", Title: "Codex", Command: "codex-as-claude"},
	}, nil)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	o.opts.AssistantsRegistry = reg
	o.opts.Config.Client = config.ClientConfig{Command: "client-cli", ExtraArgs: []string{"--verbose-x", "--model", "client-model", "--resume", "abc"}, Reasoning: "high"}
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	return o, store
}

func attachProfile(t *testing.T, o *Orchestrator, opID, stageID string, profile assistants.ProfileID) assistants.TaskID {
	t.Helper()
	id, err := o.Attach(context.Background(), AttachRequest{OperationID: opID, StageID: stageID, ProfileID: profile, Instruction: "look", Actor: userActor})
	if err != nil {
		t.Fatalf("Attach %s on %s: %v", profile, stageID, err)
	}
	return id
}

// taskExecutor reads what a task froze at admission: the resolved command and its binding.
func taskExecutor(t *testing.T, store *state.Store, id assistants.TaskID) (string, assistants.ExecutionBinding) {
	t.Helper()
	tk := store.Snapshot().Assistants.Tasks[id]
	if tk == nil || tk.Execution == nil {
		t.Fatalf("task %s has no execution binding: %+v", id, tk)
	}
	return tk.ProfileSnapshot.Command, *tk.Execution
}

func binding(source assistants.CommandSource, args []string, reasoning string) assistants.ExecutionBinding {
	return assistants.ExecutionBinding{Source: source, ExtraArgs: args, Reasoning: reasoning, RuntimeContext: assistants.RuntimeContextIsolated}
}

// A helper without its own command runs the executor of where it is attached; a profile's own command wins. Only
// the client's executor carries its allowlisted model arguments, and a profile's own command gets no reasoning.
func TestAdmission_ExecutorFollowsWhereTheTaskRuns(t *testing.T) {
	o, store := newExecutionTestOrchestrator(t, flow.Stage{ID: plainStageID}, flow.Stage{ID: "dyn"})
	// "dyn" names its command only in the runtime registry, like a stage added during the run.
	stages := o.reg().Stages()
	for i := range stages {
		if stages[i].ID == "dyn" {
			stages[i].Command = "dyn-cmd"
		}
	}
	o.graph.Replace(stages)

	cases := []struct {
		name, stage string
		profile     assistants.ProfileID
		wantCmd     string
		want        assistants.ExecutionBinding
	}{
		{"role-only on a stage with its own command", assistantsTestStageID, "role", "claude", binding(assistants.CommandSourceStage, nil, "high")},
		{"builtin default on a stage with its own command", assistantsTestStageID, assistants.DefaultProfileID, "claude", binding(assistants.CommandSourceStage, nil, "high")},
		{"role-only on a stage without a command", plainStageID, "role", "client-cli", binding(assistants.CommandSourceClient, clientModelArgs, "high")},
		{"role-only on a stage added during the run", "dyn", "role", "dyn-cmd", binding(assistants.CommandSourceStage, nil, "high")},
		{"a profile's own command", assistantsTestStageID, "explicit", "ext-cmd", binding(assistants.CommandSourceProfile, nil, "")},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := attachProfile(t, o, fmt.Sprintf("op-%d", i), tc.stage, tc.profile)
			cmd, got := taskExecutor(t, store, id)
			if cmd != tc.wantCmd || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("executor = %q %+v, want %q %+v", cmd, got, tc.wantCmd, tc.want)
			}
		})
	}

	t.Run("run agent", func(t *testing.T) {
		res, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{OperationID: "op-run", ProfileID: "role", Title: "R", Instruction: "x", Actor: userActor})
		if err != nil {
			t.Fatalf("AddRunAgent: %v", err)
		}
		cmd, got := taskExecutor(t, store, res.TaskID)
		if want := binding(assistants.CommandSourceClient, clientModelArgs, "high"); cmd != "client-cli" || !reflect.DeepEqual(got, want) {
			t.Fatalf("run agent executor = %q %+v, want client-cli %+v", cmd, got, want)
		}
	})
}

// A delegated child without its own command runs its parent's executor — with the parent's arguments and recipe
// identity, never the current ones — and one with its own command gets its own.
func TestDelegation_ChildInheritsParentExecutor(t *testing.T) {
	o, store := newExecutionTestOrchestrator(t, flow.Stage{ID: plainStageID})
	o.opts.RecipeIdentity = func(cmd string) string {
		if cmd == "ext-cmd" {
			return "id-1"
		}
		return ""
	}
	inheriting := attachProfile(t, o, "op-inheriting", plainStageID, "role")
	explicit := attachProfile(t, o, "op-explicit", assistantsTestStageID, "explicit")
	o.opts.RecipeIdentity = func(cmd string) string {
		if cmd == "ext-cmd" {
			return "id-2"
		}
		return ""
	}

	children, err := o.admitChildren(context.Background(), "op-children-1", inheriting, []assistants.DelegateRequest{
		{ProfileID: "role", Instruction: "a"}, {ProfileID: "explicit", Instruction: "b"},
	}, userActor, "", nil)
	if err != nil {
		t.Fatalf("admitChildren: %v", err)
	}
	if cmd, got := taskExecutor(t, store, children[0]); cmd != "client-cli" || !reflect.DeepEqual(got, binding(assistants.CommandSourceParent, clientModelArgs, "high")) {
		t.Fatalf("child of an inheriting parent = %q %+v", cmd, got)
	}
	want := binding(assistants.CommandSourceProfile, nil, "")
	want.RecipeIdentity = "id-2"
	if cmd, got := taskExecutor(t, store, children[1]); cmd != "ext-cmd" || !reflect.DeepEqual(got, want) {
		t.Fatalf("child with its own command = %q %+v, want ext-cmd %+v", cmd, got, want)
	}

	grandchildren, err := o.admitChildren(context.Background(), "op-children-2", explicit, []assistants.DelegateRequest{{ProfileID: "role", Instruction: "c"}}, userActor, "", nil)
	if err != nil {
		t.Fatalf("admitChildren: %v", err)
	}
	want = binding(assistants.CommandSourceParent, nil, "")
	want.RecipeIdentity = "id-1"
	if cmd, got := taskExecutor(t, store, grandchildren[0]); cmd != "ext-cmd" || !reflect.DeepEqual(got, want) {
		t.Fatalf("child of an explicit parent = %q %+v, want ext-cmd %+v", cmd, got, want)
	}
}

// The binding is the one record of a task's runtime context: the snapshot of a profile that sets one keeps none,
// in memory or in the journal.
func TestAdmission_RuntimeContextOnlyInTheBinding(t *testing.T) {
	o, store := newExecutionTestOrchestrator(t)
	wrapper := assistants.RuntimeContextWrapper
	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{{ID: "ext", Command: "ext-cmd", RuntimeContext: &wrapper}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	o.opts.AssistantsRegistry = reg
	id := attachProfile(t, o, "op-1", assistantsTestStageID, "ext")

	if _, b := taskExecutor(t, store, id); b.RuntimeContext != wrapper {
		t.Fatalf("binding runtime context = %q, want wrapper", b.RuntimeContext)
	}
	if rc := store.Snapshot().Assistants.Tasks[id].ProfileSnapshot.RuntimeContext; rc != nil {
		t.Fatalf("profile snapshot keeps runtime context %q next to the binding", *rc)
	}
	journal, err := os.ReadFile(filepath.Join(o.opts.RunDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(journal), `"RuntimeContext"`) {
		t.Fatal("task_created journals the profile's RuntimeContext next to execution.runtime_context")
	}
}

// client.command left unset still names an executor: the builtin claude, never an empty snapshot the executor
// would fill in on its own. A resolution with no executor at all is a typed admission rejection.
func TestAdmission_NeverAnEmptyExecutor(t *testing.T) {
	o, store := newExecutionTestOrchestrator(t, flow.Stage{ID: plainStageID})
	o.opts.Config.Client.Command = ""
	id := attachProfile(t, o, "op-1", plainStageID, "role")
	if cmd, got := taskExecutor(t, store, id); cmd != config.ClaudeCommand || got.Source != assistants.CommandSourceClient {
		t.Fatalf("executor = %q %+v, want the builtin claude from the client", cmd, got)
	}

	_, _, err := o.admitExecution(assistants.Profile{ID: "role"}, assistants.ExecutionInputs{})
	var rejected *state.OperationRejectedError
	if !errors.As(err, &rejected) || rejected.Code != assistants.CodeCapabilityUnavailable {
		t.Fatalf("err = %v, want a capability_unavailable rejection", err)
	}
}

// Availability is decided for the executor a helper resolves to: on the host only the codex read-only adapter may
// run a helper (OS sandbox), in a container every command may (no OS guarantee).
func TestAdmission_HelperPolicyJudgesTheResolvedCommand(t *testing.T) {
	t.Run("host", func(t *testing.T) {
		t.Setenv("AFM_IN_DOCKER", "")
		t.Cleanup(config.SwapContainerMarkerPresent(func() bool { return false }))
		o, store := newExecutionTestOrchestrator(t)
		o.helperAccess = nil

		_, err := o.Attach(context.Background(), AttachRequest{OperationID: "op-claude", StageID: assistantsTestStageID, ProfileID: "role", Instruction: "x", Actor: userActor})
		var rejected *state.OperationRejectedError
		if !errors.As(err, &rejected) || rejected.Code != assistants.CodeCapabilityUnavailable ||
			!strings.Contains(rejected.Reason, `"claude"`) || !strings.Contains(rejected.Reason, "source stage") {
			t.Fatalf("role-only helper inheriting claude on the host: err = %v, want a capability_unavailable rejection naming the command and its source", err)
		}
		id := attachProfile(t, o, "op-codex", assistantsTestStageID, "codex")
		if got := store.Snapshot().Assistants.Tasks[id].ProfileSnapshot.ReadonlyEnforcement; got != assistants.ReadonlyEnforcementOS {
			t.Fatalf("codex adapter enforcement = %q, want os", got)
		}
	})
	t.Run("container", func(t *testing.T) {
		t.Cleanup(config.SwapContainerMarkerPresent(func() bool { return true }))
		o, store := newExecutionTestOrchestrator(t)
		o.helperAccess = nil
		id := attachProfile(t, o, "op-claude", assistantsTestStageID, "role")
		if got := store.Snapshot().Assistants.Tasks[id].ProfileSnapshot.ReadonlyEnforcement; got != assistants.ReadonlyEnforcementNone {
			t.Fatalf("enforcement in a container = %q, want none", got)
		}
	})
}

// "No self elevation" compares the executors the parent and the child actually resolve to: a role-only child runs
// its parent's executor and is never weaker; a child with a weaker command of its own is refused.
func TestDelegation_NoSelfElevationComparesResolvedExecutors(t *testing.T) {
	o, _ := newExecutionTestOrchestrator(t)
	o.helperAccess = func(cmd string) (string, assistants.ReadonlyEnforcement) {
		if cmd == "ext-cmd" {
			return "", assistants.ReadonlyEnforcementNone
		}
		return allowHelpersOS(cmd)
	}
	parent := attachProfile(t, o, "op-parent", assistantsTestStageID, "role")
	self := state.Actor{Kind: string(assistants.OriginAssistant), ID: string(parent)}

	_, err := o.admitChildren(context.Background(), "op-weaker", parent, []assistants.DelegateRequest{{ProfileID: "explicit", Instruction: "x"}}, self, "", nil)
	var rejected *state.OperationRejectedError
	if !errors.As(err, &rejected) || rejected.Code != assistants.CodeExecutorNotPermitted {
		t.Fatalf("weaker child: err = %v, want executor_not_permitted", err)
	}
	if _, err := o.admitChildren(context.Background(), "op-inheriting", parent, []assistants.DelegateRequest{{ProfileID: "role", Instruction: "x"}}, self, "", nil); err != nil {
		t.Fatalf("inheriting child: %v", err)
	}
}

// The binding reaches the helper's own process: the allowlisted model pair after the stream-json defaults (no other
// client flag), the reasoning, and the runtime context; a profile's own command gets neither arguments nor reasoning.
func TestHelperExecutorConfig_AppliesTheTaskBinding(t *testing.T) {
	o, _ := newExecutionTestOrchestrator(t, flow.Stage{ID: plainStageID})
	inheriting := attachProfile(t, o, "op-inheriting", plainStageID, "role")
	explicit := attachProfile(t, o, "op-explicit", assistantsTestStageID, "explicit")
	wrapper := assistants.RuntimeContextWrapper
	o.opts.Config.Collaboration.RuntimeContext = &wrapper
	wrapped := attachProfile(t, o, "op-wrapped", plainStageID, "role")

	cfg := o.helperExecutorConfig(inheriting, "client-cli")
	if want := executor.HelperClaudeArgs(clientModelArgs); !slices.Equal(cfg.ExtraArgs, want) || cfg.Reasoning != "high" || cfg.HelperWrapperRuntime {
		t.Fatalf("inheriting helper: args %v reasoning %q wrapper %v, want %v high false", cfg.ExtraArgs, cfg.Reasoning, cfg.HelperWrapperRuntime, want)
	}
	if slices.Contains(cfg.ExtraArgs, "--verbose-x") || slices.Contains(cfg.ExtraArgs, "--resume") {
		t.Fatalf("helper inherited a stage-only client flag: %v", cfg.ExtraArgs)
	}
	if cfg := o.helperExecutorConfig(explicit, "ext-cmd"); cfg.ExtraArgs != nil || cfg.Reasoning != "" || cfg.HelperWrapperRuntime {
		t.Fatalf("explicit helper: args %v reasoning %q wrapper %v, want none", cfg.ExtraArgs, cfg.Reasoning, cfg.HelperWrapperRuntime)
	}
	if cfg := o.helperExecutorConfig(wrapped, "client-cli"); !cfg.HelperWrapperRuntime {
		t.Fatal("collaboration.runtime_context: wrapper did not reach the helper's process")
	}
	if cfg := o.helperExecutorConfig(inheriting, "client-cli"); cfg.HelperWrapperRuntime {
		t.Fatal("a later runtime_context change reached a task admitted before it")
	}
}

// The format repair of a helper's answer is a process of the same task: same executor, same binding.
func TestHelperOutputRecovery_FormatterRunsTheTaskExecutor(t *testing.T) {
	o, _ := newExecutionTestOrchestrator(t, flow.Stage{ID: plainStageID})
	activateStage(t, o, plainStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: recoveryHelperRaw}, nil
	}
	formatted := make(chan executor.Config, 1)
	o.runOutputFormat = func(ctx context.Context, cfg executor.Config, prompt, logFile, resultFile string) ([]byte, error) {
		select {
		case formatted <- cfg:
		default:
		}
		return []byte(recoveryHelperFixed), nil
	}
	attachProfile(t, o, "op-1", plainStageID, "role")
	select {
	case cfg := <-formatted:
		if cfg.Command != "client-cli" || !slices.Equal(cfg.ExtraArgs, executor.HelperClaudeArgs(clientModelArgs)) || cfg.Reasoning != "high" {
			t.Fatalf("formatter = %q %v %q, want the task's executor and binding", cfg.Command, cfg.ExtraArgs, cfg.Reasoning)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("formatter did not run")
	}
	o.concurrency.WaitAgents()
}

// A failed helper process says why and with which executor: the CLI reporting an error (repair never runs on it)
// and an executable that did not start read differently, both naming role, command and source.
func TestHelperFailureReason_NamesExecutorAndKind(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantKind string
	}{
		{"CLI error", &executor.CLIReportedError{Subtype: "error_during_execution", Message: "Not logged in · Please run /login"}, "helper CLI reported an error"},
		{"start failure", fmt.Errorf("%w: missing-cli: exec: not found", executor.ErrAgentStart), "helper executable did not start"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newExecutionTestOrchestrator(t)
			activateStage(t, o, assistantsTestStageID)
			o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				return executor.HelperOutcome{ProcessErr: tc.err}, nil
			}
			var formats atomic.Int32
			o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
				formats.Add(1)
				return nil, errors.New("unexpected")
			}
			id := attachProfile(t, o, "op-1", assistantsTestStageID, "explicit")
			tk := waitForTaskStatus(t, store, id, assistants.StatusFailed, 3*time.Second)
			for _, want := range []string{tc.wantKind, "role explicit", "command ext-cmd", "source profile"} {
				if !strings.Contains(tk.StatusReason, want) {
					t.Errorf("reason %q lacks %q", tk.StatusReason, want)
				}
			}
			o.concurrency.WaitAgents()
			if formats.Load() != 0 {
				t.Fatal("output repair ran on a failed process")
			}
		})
	}
}

// A failure reason names the executor first and redacts its detail before cutting it to length, so a secret cut in
// half cannot slip through as an unredactable prefix.
func TestHelperProtocolReason_RedactsBeforeTruncating(t *testing.T) {
	o, _ := newExecutionTestOrchestrator(t)
	const secret = "sk-test-0123456789abcdef"
	o.opts.SecretValues = []string{secret}
	task := assistants.Task{ProfileID: "explicit"}
	exec := assistants.Execution{Command: "ext-cmd", Binding: assistants.ExecutionBinding{Source: assistants.CommandSourceProfile}}
	head := protocolErrorPrefix + "result contract not met (role explicit, command ext-cmd, source profile): "
	// The cut falls ten characters into the secret.
	detail := strings.Repeat("x", maxFailureReasonLen-len(head)-10) + secret + strings.Repeat("y", 50)

	reason := o.helperProtocolReason(task, exec, detail)
	if !strings.HasPrefix(reason, head) || len([]rune(reason)) > maxFailureReasonLen {
		t.Fatalf("reason = %q (%d runes), want it to start with %q and stay within %d", reason, len([]rune(reason)), head, maxFailureReasonLen)
	}
	if strings.Contains(reason, secret[:6]) {
		t.Fatalf("reason %q keeps part of the secret", reason)
	}
}

// The same through the real helper executor: a CLI answering with a structured is_error result — whether it then
// exits 0 or not — fails the task with the CLI's message and no repair.
func TestHelperFailureReason_RealCLIReportedError(t *testing.T) {
	for _, exit := range []int{0, 1} {
		t.Run(fmt.Sprintf("exit %d", exit), func(t *testing.T) {
			o, store := newExecutionTestOrchestrator(t)
			activateStage(t, o, assistantsTestStageID)
			script := filepath.Join(t.TempDir(), "not-logged-in.sh")
			body := fmt.Sprintf("#!/bin/sh\ncat >/dev/null\necho '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":true,\"result\":\"Not logged in · Please run /login\"}'\nexit %d\n", exit)
			if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			o.runHelperAgent = o.execHelperAgent
			var formats atomic.Int32
			o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
				formats.Add(1)
				return nil, errors.New("unexpected")
			}
			reg, err := assistants.BuildRegistry(nil, []assistants.Profile{{ID: "external", Title: "External", Command: script}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			o.opts.AssistantsRegistry = reg
			id := attachProfile(t, o, "op-1", assistantsTestStageID, "external")
			tk := waitForTaskStatus(t, store, id, assistants.StatusFailed, 10*time.Second)
			for _, want := range []string{"helper CLI reported an error", "Not logged in", "role external", "source profile"} {
				if !strings.Contains(tk.StatusReason, want) {
					t.Errorf("reason %q lacks %q", tk.StatusReason, want)
				}
			}
			o.concurrency.WaitAgents()
			if formats.Load() != 0 {
				t.Fatal("output repair ran on a CLI error")
			}
		})
	}
}

// Repeating an attach after the config changed answers with the task as it was created, binding included.
func TestAttach_RepeatAfterConfigChangeKeepsTheBinding(t *testing.T) {
	o, store := newExecutionTestOrchestrator(t, flow.Stage{ID: plainStageID})
	first := attachProfile(t, o, "op-1", plainStageID, "role")
	o.opts.Config.Client.ExtraArgs = []string{"--model", "other-model"}
	if again := attachProfile(t, o, "op-1", plainStageID, "role"); again != first {
		t.Fatalf("repeat answered %s, want %s", again, first)
	}
	if _, got := taskExecutor(t, store, first); !slices.Equal(got.ExtraArgs, clientModelArgs) {
		t.Fatalf("binding args = %v, want the original %v", got.ExtraArgs, clientModelArgs)
	}
}

// A retried child admission, e.g. an accepted attach_assistant proposal for a task delivered again, answers with
// the children it committed and their original binding, whatever changed since: the config and the parent's status.
// Another request under the same id is a conflict.
func TestDelegation_RepeatAfterConfigChangeReturnsTheOriginalChildren(t *testing.T) {
	o, store := newExecutionTestOrchestrator(t, flow.Stage{ID: plainStageID})
	parent := attachProfile(t, o, "op-parent", plainStageID, "role")
	children := []assistants.DelegateRequest{{ProfileID: "role", Title: "Child", Instruction: "a"}, {ProfileID: "explicit", Instruction: "b"}}
	first, err := o.admitChildren(context.Background(), "op-children", parent, children, userActor, "", nil)
	if err != nil {
		t.Fatalf("admitChildren: %v", err)
	}
	_, before := taskExecutor(t, store, first[0])

	o.concurrency.WaitAgents() // the config changes between runs, never under a running turn
	wrapper := assistants.RuntimeContextWrapper
	o.opts.Config.Collaboration.RuntimeContext = &wrapper
	o.opts.Config.Client.ExtraArgs = []string{"--model", "other-model"}
	repeat := func(when string) {
		t.Helper()
		again, err := o.admitChildren(context.Background(), "op-children", parent, children, userActor, "", nil)
		if err != nil || !slices.Equal(again, first) {
			t.Fatalf("repeat %s = %v, %v; want the original children %v", when, again, err, first)
		}
		if _, after := taskExecutor(t, store, first[0]); !reflect.DeepEqual(after, before) {
			t.Fatalf("binding after the repeat %s = %+v, want the original %+v", when, after, before)
		}
	}
	repeat("after the config change")
	if err := o.CancelTask(context.Background(), parent); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	repeat("after the parent finished")
	for name, other := range map[string][]assistants.DelegateRequest{
		"another instruction": {{ProfileID: "role", Title: "Child", Instruction: "changed"}, children[1]},
		"fewer children":      children[:1],
	} {
		if _, err := o.admitChildren(context.Background(), "op-children", parent, other, userActor, "", nil); !errors.Is(err, state.ErrOperationIDConflict) {
			t.Errorf("%s under the same id: err = %v, want an operation id conflict", name, err)
		}
	}
}

// A retried child admission is compared with the whole committed request — every child, the actor kind, the
// parent's transition and its continuation; only the same request gets the original children back.
func TestDelegation_RepeatMustMatchTheWholeRequest(t *testing.T) {
	o, _ := newExecutionTestOrchestrator(t, flow.Stage{ID: plainStageID})
	parent := attachProfile(t, o, "op-parent", plainStageID, "role")
	otherParent := attachProfile(t, o, "op-other-parent", plainStageID, "role")
	type request struct {
		parent       assistants.TaskID
		children     []assistants.DelegateRequest
		actor        state.Actor
		parentTo     state.TaskStatusChangedPayload
		continuation *assistants.Continuation
	}
	admit := func(op string, r request) ([]assistants.TaskID, error) {
		return o.admitChildrenWithParentChange(context.Background(), op, r.parent, r.children, r.actor, r.parentTo, r.continuation, nil)
	}
	children := []assistants.DelegateRequest{
		{ProfileID: "role", Title: "Child", Instruction: "a"},
		{ProfileID: "explicit", Instruction: "b", Mode: assistants.ModeRequired, Trigger: assistants.TriggerCheckpoints},
	}
	withChild := func(change func(*assistants.DelegateRequest)) []assistants.DelegateRequest {
		out := slices.Clone(children)
		change(&out[0])
		return out
	}
	// Without a parent change first: the full request below may let the parent finish once its children do.
	bare := request{parent, children, userActor, state.TaskStatusChangedPayload{}, nil}
	bareIDs, err := admit("op-bare", bare)
	if err != nil {
		t.Fatalf("admit without a parent change: %v", err)
	}
	full := request{parent, children, userActor,
		state.TaskStatusChangedPayload{Status: string(assistants.StatusWaitingChildren), Turn: 3},
		&assistants.Continuation{Trigger: assistants.TriggerOnCompletion, Note: "wait for both"}}
	fullIDs, err := admit("op-full", full)
	if err != nil {
		t.Fatalf("admit with a parent change: %v", err)
	}

	changed := func(change func(*request)) request {
		r := full
		change(&r)
		return r
	}
	for _, tc := range []struct {
		name string
		op   string
		r    request
		want []assistants.TaskID // nil: an operation id conflict
	}{
		{"identical", "op-full", full, fullIDs},
		{"defaults written out", "op-full", changed(func(r *request) {
			r.children = withChild(func(c *assistants.DelegateRequest) {
				c.Mode, c.Trigger = assistants.ModeAdvisory, assistants.TriggerOneShot
			})
		}), fullIDs},
		{"identical without a parent change", "op-bare", bare, bareIDs},
		{"another parent", "op-full", changed(func(r *request) { r.parent = otherParent }), nil},
		{"fewer children", "op-full", changed(func(r *request) { r.children = children[:1] }), nil},
		{"more children", "op-full", changed(func(r *request) { r.children = append(slices.Clone(children), children[0]) }), nil},
		{"children reordered", "op-full", changed(func(r *request) { r.children = []assistants.DelegateRequest{children[1], children[0]} }), nil},
		{"another profile", "op-full", changed(func(r *request) {
			r.children = withChild(func(c *assistants.DelegateRequest) { c.ProfileID = "codex" })
		}), nil},
		{"another title", "op-full", changed(func(r *request) { r.children = withChild(func(c *assistants.DelegateRequest) { c.Title = "Other" }) }), nil},
		{"another instruction", "op-full", changed(func(r *request) {
			r.children = withChild(func(c *assistants.DelegateRequest) { c.Instruction = "changed" })
		}), nil},
		{"another mode", "op-full", changed(func(r *request) {
			r.children = withChild(func(c *assistants.DelegateRequest) { c.Mode = assistants.ModeRequired })
		}), nil},
		{"another trigger", "op-full", changed(func(r *request) {
			r.children = withChild(func(c *assistants.DelegateRequest) { c.Trigger = assistants.TriggerCheckpoints })
		}), nil},
		{"another actor kind", "op-full", changed(func(r *request) {
			r.actor = state.Actor{Kind: string(assistants.OriginAssistant), ID: string(parent)}
		}), nil},
		{"parent change omitted", "op-full", changed(func(r *request) { r.parentTo = state.TaskStatusChangedPayload{} }), nil},
		{"another parent status", "op-full", changed(func(r *request) { r.parentTo.Status = string(assistants.StatusQueued) }), nil},
		{"another turn", "op-full", changed(func(r *request) { r.parentTo.Turn = 4 }), nil},
		{"continuation omitted", "op-full", changed(func(r *request) { r.continuation = nil }), nil},
		{"checkpoints instead of on_completion", "op-full", changed(func(r *request) {
			r.continuation = &assistants.Continuation{Trigger: assistants.TriggerCheckpoints, Note: full.continuation.Note}
		}), nil},
		{"another note", "op-full", changed(func(r *request) {
			r.continuation = &assistants.Continuation{Trigger: full.continuation.Trigger, Note: "something else"}
		}), nil},
		{"parent change added", "op-bare", changed(func(r *request) { r.continuation = nil }), nil},
		{"continuation added", "op-bare", request{parent, children, userActor, state.TaskStatusChangedPayload{}, full.continuation}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := admit(tc.op, tc.r)
			if tc.want == nil {
				if !errors.Is(err, state.ErrOperationIDConflict) {
					t.Fatalf("repeat = %v, %v; want an operation id conflict", got, err)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("repeat = %v, %v; want the original children %v", got, err, tc.want)
			}
		})
	}
}

// A required helper and the watchers resolve their executor like a one_shot one, and the turn runs it.
func TestTriggersAndModes_InheritTheExecutor(t *testing.T) {
	o, store := newExecutionTestOrchestrator(t, flow.Stage{ID: plainStageID})
	activateStage(t, o, plainStageID)
	ran := make(chan string, 1)
	o.runHelperAgent = func(_ context.Context, _, _, cmd, _, _, _ string) (executor.HelperOutcome, error) {
		select {
		case ran <- cmd:
		default:
		}
		return passResultOutcome(), nil
	}
	for i, tc := range []struct {
		mode    assistants.Mode
		trigger assistants.Trigger
	}{
		{assistants.ModeRequired, assistants.TriggerOneShot},
		{assistants.ModeAdvisory, assistants.TriggerCheckpoints},
		{assistants.ModeRequired, assistants.TriggerOnCompletion},
	} {
		id, err := o.Attach(context.Background(), AttachRequest{OperationID: fmt.Sprintf("op-%d", i), StageID: plainStageID, ProfileID: "role",
			Instruction: "x", Mode: tc.mode, Trigger: tc.trigger, Actor: userActor})
		if err != nil {
			t.Fatalf("Attach %s/%s: %v", tc.mode, tc.trigger, err)
		}
		if cmd, got := taskExecutor(t, store, id); cmd != "client-cli" || got.Source != assistants.CommandSourceClient {
			t.Fatalf("%s/%s executor = %q %+v", tc.mode, tc.trigger, cmd, got)
		}
	}
	select {
	case cmd := <-ran:
		if cmd != "client-cli" {
			t.Fatalf("required one_shot turn ran %q, want client-cli", cmd)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the one_shot helper did not run")
	}
	o.concurrency.WaitAgents()
}

// A task of an older journal has no binding: it runs the default executor of the current flow and config,
// resolved like a new task's — the stage's command, else the client's; a run agent the client's; a child its
// parent's — and its recipe is never compared.
func TestTaskExecution_LegacyTasksRunTheCurrentDefault(t *testing.T) {
	o, store := newExecutionTestOrchestrator(t, flow.Stage{ID: plainStageID})
	o.opts.RecipeIdentity = func(string) string { return "id-now" }
	appendLegacyTask(t, store, "t-stage", assistantsTestStageID, "", "", "queued")
	appendLegacyTask(t, store, "t-plain", plainStageID, "", "", "waiting_parent")
	appendLegacyTask(t, store, "t-run", "", "", "", "waiting_parent")
	appendLegacyTask(t, store, "t-child", plainStageID, "t-plain", "", "waiting_parent")
	appendLegacyTask(t, store, "t-named", plainStageID, "", "ext-cmd", "waiting_parent")

	rs := store.Snapshot()
	for _, tc := range []struct {
		id      assistants.TaskID
		wantCmd string
		want    assistants.ExecutionBinding
	}{
		{"t-stage", "claude", binding(assistants.CommandSourceStage, nil, "high")},
		{"t-plain", "client-cli", binding(assistants.CommandSourceClient, clientModelArgs, "high")},
		{"t-run", "client-cli", binding(assistants.CommandSourceClient, clientModelArgs, "high")},
		{"t-child", "client-cli", binding(assistants.CommandSourceParent, clientModelArgs, "high")},
		{"t-named", "ext-cmd", binding(assistants.CommandSourceProfile, nil, "")},
	} {
		got := o.taskExecution(rs, rs.Assistants.Tasks[tc.id].Task)
		got.Binding.RecipeIdentity = ""
		if got.Command != tc.wantCmd || !reflect.DeepEqual(got.Binding, tc.want) {
			t.Errorf("%s = %q %+v, want %q %+v", tc.id, got.Command, got.Binding, tc.wantCmd, tc.want)
		}
	}

	ran := make(chan string, 1)
	o.runHelperAgent = func(_ context.Context, _, _, cmd, _, _, _ string) (executor.HelperOutcome, error) {
		select {
		case ran <- cmd:
		default:
		}
		return passResultOutcome(), nil
	}
	o.maybeScheduleTask(context.Background(), "t-stage")
	select {
	case cmd := <-ran:
		if cmd != "claude" {
			t.Fatalf("legacy turn ran %q, want the stage's claude", cmd)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("legacy task did not run: its recipe must not be compared")
	}
	waitForTaskStatus(t, store, "t-stage", assistants.StatusCompleted, 3*time.Second)
}

// appendLegacyTask journals a task the way an older afm did: no execution binding, the profile's own (possibly
// empty) command in the snapshot.
func appendLegacyTask(t *testing.T, store *state.Store, id, stageID, parent, cmd, status string) {
	t.Helper()
	payload := map[string]any{
		"profile_id": "role", "profile_snapshot": map[string]any{"ID": "role", "Title": "Role", "Command": cmd},
		"title": "Legacy", "instruction": "x", "mode": "advisory", "trigger": "one_shot", "policy_rev": "rev-1",
		"max_depth_at_create": 3, "created_by": "user",
	}
	if parent != "" {
		payload["parent_task_id"] = parent
	}
	if stageID == "" {
		payload["scope"] = "run"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendControl(&state.Control{Kind: "task_created", SchemaVersion: state.ControlSchemaVersion, OperationID: "legacy-" + id,
		Actor: userActor, Target: state.Target{TaskID: id, StageID: stageID}, Payload: raw}); err != nil {
		t.Fatalf("append legacy task_created: %v", err)
	}
	if status == "waiting_parent" {
		return
	}
	raw, _ = json.Marshal(state.TaskStatusChangedPayload{Status: status})
	if err := store.AppendControl(&state.Control{Kind: "task_status_changed", SchemaVersion: state.ControlSchemaVersion,
		Target: state.Target{TaskID: id}, Payload: raw}); err != nil {
		t.Fatalf("append legacy status: %v", err)
	}
}

func TestAssistantProfiles_ResolvedWhereTheyWouldRun(t *testing.T) {
	o, _ := newExecutionTestOrchestrator(t, flow.Stage{ID: plainStageID}, flow.Stage{ID: "script", Script: "true"})
	o.helperAccess = func(cmd string) (string, assistants.ReadonlyEnforcement) {
		if cmd == "ext-cmd" {
			return "not on this host", assistants.ReadonlyEnforcementNone
		}
		return allowHelpersOS(cmd)
	}
	view := func(views []assistants.ProfileView, id assistants.ProfileID) assistants.ProfileView {
		for _, v := range views {
			if v.ID == id {
				return v
			}
		}
		t.Fatalf("profile %s missing from %+v", id, views)
		return assistants.ProfileView{}
	}

	onStage, err := o.AssistantProfiles(assistantsTestStageID)
	if err != nil {
		t.Fatal(err)
	}
	if v := view(onStage, assistants.DefaultProfileID); v.Command != "claude" || v.CommandSource != assistants.CommandSourceStage || !v.Available ||
		v.RuntimeContext != assistants.RuntimeContextIsolated || v.ReadonlyEnforcement != assistants.ReadonlyEnforcementOS {
		t.Fatalf("default on impl = %+v", v)
	}
	if v := view(onStage, "explicit"); v.Command != "ext-cmd" || v.CommandSource != assistants.CommandSourceProfile || v.Available || v.UnavailableReason != "not on this host" {
		t.Fatalf("explicit on impl = %+v", v)
	}
	onPlain, err := o.AssistantProfiles(plainStageID)
	if err != nil {
		t.Fatal(err)
	}
	if v := view(onPlain, "role"); v.Command != "client-cli" || v.CommandSource != assistants.CommandSourceClient {
		t.Fatalf("role on a stage without a command = %+v", v)
	}
	runLevel, err := o.AssistantProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	if v := view(runLevel, "role"); v.Command != "client-cli" || v.CommandSource != assistants.CommandSourceClient {
		t.Fatalf("role as a run agent = %+v", v)
	}

	for stage, code := range map[string]assistants.ErrorCode{"nope": assistants.CodeNotFound, "script": assistants.CodeInvalidRequest} {
		_, err := o.AssistantProfiles(stage)
		var rejected *state.OperationRejectedError
		if !errors.As(err, &rejected) || rejected.Code != code {
			t.Errorf("AssistantProfiles(%q) err = %v, want %s", stage, err, code)
		}
	}
}

// ---- real Run: restart, shared command slot ----

const execRunStageID = "work"

// heldStageRunner is the stage agent of an execution run: it holds its turn until released, then completes it.
type heldStageRunner struct {
	entered  chan struct{}
	release  chan struct{}
	returned atomic.Bool
}

func newHeldStageRunner() *heldStageRunner {
	return &heldStageRunner{entered: make(chan struct{}, 4), release: make(chan struct{})}
}

func (r *heldStageRunner) RunPlanning(context.Context, string, string, string, string) error {
	return errors.New("unexpected planning turn")
}

func (r *heldStageRunner) RunJSONQuery(context.Context, string) ([]byte, error) { return nil, nil }

func (r *heldStageRunner) RunAgent(ctx context.Context, _, _, _, logFile string) error {
	r.entered <- struct{}{}
	select {
	case <-r.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.returned.Store(true)
	return os.WriteFile(filepath.Join(filepath.Dir(logFile), "execution_summary.md"), []byte("## Summary\ndone\n"), 0o644)
}

// helperCall is what one helper turn would run: the command and the executor config built for it.
type helperCall struct {
	taskID string
	cmd    string
	cfg    executor.Config
}

// execRun is one afm process of a run over dir: a real Run with the stage held by its agent and every helper turn
// recorded, then answered by answer (nil: wait for the run's shutdown, as a turn cut short by Ctrl+C).
type execRun struct {
	o     *Orchestrator
	store *state.Store
	stage *heldStageRunner
	calls chan helperCall
	// stop shuts the process down like Ctrl+C: the run context ends, Run returns after its agents, the journal closes.
	stop func()
}

func startExecRun(t *testing.T, dir string, cfg config.Config, identity func(string) string, resumed bool, answer func(taskID string) bool) *execRun {
	t.Helper()
	store, err := state.Open(dir, []string{execRunStageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	reg, err := assistants.BuildRegistry(assistants.BuiltinProfiles(), []assistants.Profile{
		{ID: "role", Title: "Role", Instructions: "review"},
		{ID: "recipe", Title: "Recipe", Command: "fake-recipe"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &execRun{store: store, stage: newHeldStageRunner(), calls: make(chan helperCall, 16)}
	r.o = New(Options{
		RunDir: dir, RootDir: t.TempDir(), Store: store, Config: cfg, Runner: r.stage, Prompts: DefaultPrompts(), Resumed: resumed,
		Stages:            []flow.Stage{{ID: execRunStageID, Name: "Work", Agents: []flow.AgentType{flow.AgentAuto}}},
		AssistantsEnabled: true, AssistantsRegistry: reg, RecipeIdentity: identity,
	})
	r.o.helperAccess = allowHelpersOS
	r.o.runHelperAgent = func(ctx context.Context, taskID, _, cmd, _, _, _ string) (executor.HelperOutcome, error) {
		r.calls <- helperCall{taskID: taskID, cmd: cmd, cfg: r.o.helperExecutorConfig(assistants.TaskID(taskID), cmd)}
		if answer != nil && answer(taskID) {
			return passResultOutcome(), nil
		}
		<-ctx.Done()
		return executor.HelperOutcome{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	done := make(chan struct{})
	go func() { _ = r.o.Run(ctx); close(done) }()
	r.stop = sync.OnceFunc(func() {
		cancel()
		<-done
		_ = r.store.Close()
	})
	t.Cleanup(r.stop)
	return r
}

func (r *execRun) waitStage(t *testing.T) {
	t.Helper()
	select {
	case <-r.stage.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the stage agent did not start")
	}
}

func (r *execRun) nextCall(t *testing.T) helperCall {
	t.Helper()
	select {
	case c := <-r.calls:
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("no helper turn started")
		return helperCall{}
	}
}

func (r *execRun) attach(t *testing.T, opID string, profile assistants.ProfileID) assistants.TaskID {
	t.Helper()
	return attachProfile(t, r.o, opID, execRunStageID, profile)
}

func clientConfig(model string) config.Config {
	cfg := config.Default()
	cfg.Client.ExtraArgs = []string{"--model", model}
	return cfg
}

// A turn cut short by a restart runs again with the executor the task was created with, although the client's
// model changed in between; a task created after the restart gets the new one.
func TestRestart_TaskKeepsItsExecutorAcrossConfigChange(t *testing.T) {
	dir := t.TempDir()
	first := startExecRun(t, dir, clientConfig("model-1"), nil, false, nil)
	first.waitStage(t)
	id := first.attach(t, "op-before", "role")
	if c := first.nextCall(t); !slices.Equal(c.cfg.ExtraArgs, executor.HelperClaudeArgs([]string{"--model", "model-1"})) {
		t.Fatalf("first turn args = %v", c.cfg.ExtraArgs)
	}
	first.stop()

	second := startExecRun(t, dir, clientConfig("model-2"), nil, true, func(string) bool { return true })
	resumed := second.nextCall(t)
	if resumed.taskID != string(id) || resumed.cmd != config.ClaudeCommand || !slices.Equal(resumed.cfg.ExtraArgs, executor.HelperClaudeArgs([]string{"--model", "model-1"})) {
		t.Fatalf("resumed turn = %+v, want task %s with claude and model-1", resumed, id)
	}
	waitForTaskStatus(t, second.store, id, assistants.StatusCompleted, 10*time.Second)
	fresh := second.attach(t, "op-after", "role")
	if c := second.nextCall(t); c.taskID != string(fresh) || !slices.Equal(c.cfg.ExtraArgs, executor.HelperClaudeArgs([]string{"--model", "model-2"})) {
		t.Fatalf("new task's turn = %+v, want model-2", c)
	}
}

// A journal written before bindings existed resumes its task with the current default executor.
func TestRestart_LegacyTaskRunsTheCurrentDefault(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{execRunStageID})
	if err != nil {
		t.Fatal(err)
	}
	appendLegacyTask(t, store, "t-legacy", execRunStageID, "", "", "running")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	run := startExecRun(t, dir, clientConfig("model-now"), nil, true, func(string) bool { return true })
	c := run.nextCall(t)
	if c.taskID != "t-legacy" || c.cmd != config.ClaudeCommand || !slices.Equal(c.cfg.ExtraArgs, executor.HelperClaudeArgs([]string{"--model", "model-now"})) {
		t.Fatalf("legacy turn = %+v, want claude with the current model", c)
	}
	waitForTaskStatus(t, run.store, "t-legacy", assistants.StatusCompleted, 10*time.Second)
}

// A recipe alias that names another recipe after a restart does not move a task to another model or account: the
// task fails with executor_changed and no process starts. The same recipe resumes as usual, and a task created
// after the change records the new recipe.
func TestRestart_RecipeChangeFailsTheTask(t *testing.T) {
	identity := func(id string) func(string) string {
		return func(cmd string) string {
			if cmd == "fake-recipe" {
				return id
			}
			return ""
		}
	}
	for _, tc := range []struct {
		name  string
		after string
	}{{"changed", "recipe-2"}, {"unchanged", "recipe-1"}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			first := startExecRun(t, dir, config.Default(), identity("recipe-1"), false, nil)
			first.waitStage(t)
			id := first.attach(t, "op-before", "recipe")
			first.nextCall(t)
			if _, b := taskExecutor(t, first.store, id); b.RecipeIdentity != "recipe-1" {
				t.Fatalf("recorded recipe identity = %q", b.RecipeIdentity)
			}
			first.stop()

			second := startExecRun(t, dir, config.Default(), identity(tc.after), true, func(string) bool { return true })
			second.waitStage(t)
			if tc.after == "recipe-1" {
				if c := second.nextCall(t); c.taskID != string(id) {
					t.Fatalf("resumed turn of %s, want %s", c.taskID, id)
				}
				waitForTaskStatus(t, second.store, id, assistants.StatusCompleted, 10*time.Second)
				return
			}
			tk := waitForTaskStatus(t, second.store, id, assistants.StatusFailed, 10*time.Second)
			if !strings.HasPrefix(tk.StatusReason, executorChangedPrefix) || !strings.Contains(tk.StatusReason, "(role recipe, command fake-recipe, source profile)") {
				t.Errorf("reason %q, want %q first and the helper's executor named", tk.StatusReason, executorChangedPrefix)
			}
			fresh := second.attach(t, "op-after", "recipe")
			if c := second.nextCall(t); c.taskID != string(fresh) {
				t.Fatalf("a turn of %s started, want only the new task %s", c.taskID, fresh)
			}
			if _, b := taskExecutor(t, second.store, fresh); b.RecipeIdentity != "recipe-2" {
				t.Fatalf("new task's recipe identity = %q, want recipe-2", b.RecipeIdentity)
			}
		})
	}
}

// A task of an older journal shares the semaphore of its default executor, here its stage's own command and not
// the default one: under max_parallel 1 its turn waits while that command's slot is taken, as by the stage agent.
func TestMaxParallelOne_LegacyHelperWaitsForItsStageCommand(t *testing.T) {
	const stageCmd = "stage-cli"
	o, store := newExecutionTestOrchestrator(t, flow.Stage{ID: "cmd-stage", Command: stageCmd, MaxParallel: 1})
	calls := make(chan string, 1)
	o.runHelperAgent = func(_ context.Context, _, _, cmd, _, _, _ string) (executor.HelperOutcome, error) {
		calls <- cmd
		return passResultOutcome(), nil
	}
	activateStage(t, o, "cmd-stage")
	stageSlot, err := o.concurrency.AcquireLease(context.Background(), stageCmd)
	if err != nil {
		t.Fatal(err)
	}
	appendLegacyTask(t, store, "t-legacy", "cmd-stage", "", "", "queued")
	o.maybeScheduleTask(context.Background(), "t-legacy")

	select {
	case cmd := <-calls:
		t.Fatalf("legacy helper started on %s while its stage command's slot was taken", cmd)
	case <-time.After(300 * time.Millisecond):
	}
	stageSlot.Release()
	select {
	case cmd := <-calls:
		if cmd != stageCmd {
			t.Fatalf("legacy helper ran %s, want its stage's %s", cmd, stageCmd)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("legacy helper did not start after the slot was released")
	}
	waitForTaskStatus(t, store, "t-legacy", assistants.StatusCompleted, 5*time.Second)
	o.concurrency.WaitAgents()
}

// With max_parallel: 1 a helper inheriting the stage's command shares its slot: it starts only once the stage agent
// gave it back. A helper with another command runs alongside.
func TestMaxParallelOne_InheritingHelperWaitsForTheStageAgent(t *testing.T) {
	cfg := config.Default()
	cfg.Executor.MaxParallel = 1
	run := startExecRun(t, t.TempDir(), cfg, nil, false, func(string) bool { return true })
	run.waitStage(t)
	inheriting := run.attach(t, "op-inheriting", "role")
	other := run.attach(t, "op-other", "recipe")
	if c := run.nextCall(t); c.taskID != string(other) {
		t.Fatalf("first helper turn = %s, want the one with its own command (%s) while the stage holds the slot", c.taskID, other)
	}
	waitForTaskStatus(t, run.store, other, assistants.StatusCompleted, 10*time.Second)
	select {
	case c := <-run.calls:
		t.Fatalf("helper %s started while the stage agent held the slot of its command", c.taskID)
	case <-time.After(300 * time.Millisecond):
	}
	close(run.stage.release)
	c := run.nextCall(t)
	if c.taskID != string(inheriting) || !run.stage.returned.Load() {
		t.Fatalf("turn %s started (stage returned: %v), want %s after the stage agent", c.taskID, run.stage.returned.Load(), inheriting)
	}
	waitForTaskStatus(t, run.store, inheriting, assistants.StatusCompleted, 10*time.Second)
}

// In a real run a required helper and an on_completion watcher both run the executor they inherit: the required
// one before its stage may finish, the watcher once the stage did.
func TestRun_RequiredAndWatcherRunTheInheritedExecutor(t *testing.T) {
	run := startExecRun(t, t.TempDir(), clientConfig("model-1"), nil, false, func(string) bool { return true })
	run.waitStage(t)
	attach := func(opID string, mode assistants.Mode, trigger assistants.Trigger) assistants.TaskID {
		id, err := run.o.Attach(context.Background(), AttachRequest{OperationID: opID, StageID: execRunStageID, ProfileID: "role",
			Instruction: "x", Mode: mode, Trigger: trigger, Actor: userActor})
		if err != nil {
			t.Fatalf("Attach %s/%s: %v", mode, trigger, err)
		}
		return id
	}
	required := attach("op-required", assistants.ModeRequired, assistants.TriggerOneShot)
	watcher := attach("op-watcher", assistants.ModeAdvisory, assistants.TriggerOnCompletion)
	wantArgs := executor.HelperClaudeArgs([]string{"--model", "model-1"})

	if c := run.nextCall(t); c.taskID != string(required) || c.cmd != config.ClaudeCommand || !slices.Equal(c.cfg.ExtraArgs, wantArgs) {
		t.Fatalf("required turn = %+v, want claude with model-1", c)
	}
	waitForTaskStatus(t, run.store, required, assistants.StatusCompleted, 10*time.Second)
	close(run.stage.release)
	if c := run.nextCall(t); c.taskID != string(watcher) || c.cmd != config.ClaudeCommand || !slices.Equal(c.cfg.ExtraArgs, wantArgs) {
		t.Fatalf("watcher turn = %+v, want claude with model-1", c)
	}
	waitForTaskStatus(t, run.store, watcher, assistants.StatusCompleted, 10*time.Second)
}
