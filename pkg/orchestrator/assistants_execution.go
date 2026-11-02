package orchestrator

import (
	"fmt"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)

// executorChangedPrefix starts the reason of a task whose recorded recipe no longer matches its command's wrapper.
const executorChangedPrefix = "executor_changed: "

// helperCommandAccess says whether this process may run cmd as a helper and under which read-only guarantee:
// the one availability check of admission, delegation and the profile list.
func (o *Orchestrator) helperCommandAccess(cmd string) (rejection string, enforcement assistants.ReadonlyEnforcement) {
	if o.helperAccess != nil {
		return o.helperAccess(cmd)
	}
	cfg := o.opts.Config
	return config.HelperCommandRejection(cmd, cfg), assistants.ReadonlyEnforcement(config.HelperReadonlyEnforcement(cmd, cfg))
}

// recipeIdentity is the identity of the recipe this process generates cmd's wrapper from, "" when none.
func (o *Orchestrator) recipeIdentity(cmd string) string {
	if o.opts.RecipeIdentity == nil {
		return ""
	}
	return o.opts.RecipeIdentity(cmd)
}

// clientCommand is client.command, or the builtin claude when the config names none.
func (o *Orchestrator) clientCommand() string {
	if cmd := o.opts.Config.Client.Command; cmd != "" {
		return cmd
	}
	return config.ClaudeCommand
}

// executionInputs gathers the executors a task attached to stageID ("" for a run agent) can inherit. The stage's
// own command is read from the runtime registry, so a stage added during the run counts too.
func (o *Orchestrator) executionInputs(stageID string, parent *assistants.Execution) assistants.ExecutionInputs {
	in := assistants.ExecutionInputs{
		ClientCommand:   o.clientCommand(),
		ClientModelArgs: executor.HelperModelArgs(o.opts.Config.Client.ExtraArgs),
		ClientReasoning: o.opts.Config.Client.Reasoning,
		Parent:          parent,
		RuntimeContext:  o.opts.Config.Collaboration.RuntimeContext,
		RecipeIdentity:  o.recipeIdentity,
	}
	if stageID != "" {
		if st := o.reg().Stage(stageID); st != nil {
			in.StageCommand = st.Command
		}
	}
	return in
}

// admitExecution resolves a new task's executor and checks that this process may run it. The returned profile is
// what the task freezes: the resolved command and the read-only guarantee it is admitted under. Its runtime context
// is cleared: the binding is the one record of it.
func (o *Orchestrator) admitExecution(profile assistants.Profile, in assistants.ExecutionInputs) (assistants.Profile, assistants.ExecutionBinding, error) {
	exec, err := assistants.ResolveExecution(profile, in)
	if err != nil {
		return assistants.Profile{}, assistants.ExecutionBinding{}, attachRejected(assistants.CodeCapabilityUnavailable, "profile %q: %v", profile.ID, err)
	}
	rejection, enforcement := o.helperCommandAccess(exec.Command)
	if rejection != "" {
		return assistants.Profile{}, assistants.ExecutionBinding{}, attachRejected(assistants.CodeCapabilityUnavailable, "profile %q (%s): %s", profile.ID, executorLabel(exec), rejection)
	}
	profile.Command = exec.Command
	profile.ReadonlyEnforcement = enforcement
	profile.RuntimeContext = nil
	return profile, exec.Binding, nil
}

// taskExecution is the executor every turn of t runs, and its format repair: the binding committed with the task,
// or — for a task of an older journal — the default executor of the current flow and config, resolved like a new
// task's but recorded nowhere.
func (o *Orchestrator) taskExecution(rs state.RunState, t assistants.Task) assistants.Execution {
	if t.Execution != nil {
		return assistants.Execution{Command: t.ProfileSnapshot.Command, Binding: *t.Execution.Clone()}
	}
	var parent *assistants.Execution
	if t.ParentTaskID != nil {
		if p, ok := rs.Assistants.Tasks[*t.ParentTaskID]; ok {
			pe := o.taskExecution(rs, p.Task)
			parent = &pe
		}
	}
	// Cannot fail: the client command is never empty, and neither is a parent's.
	exec, _ := assistants.ResolveExecution(t.ProfileSnapshot, o.executionInputs(t.RootStageID, parent))
	return exec
}

// taskExecutionByID is taskExecution of a task known by id; an unknown task runs its command as given, unbound.
func (o *Orchestrator) taskExecutionByID(taskID assistants.TaskID, cmd string) assistants.Execution {
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok {
		return assistants.Execution{Command: cmd}
	}
	return o.taskExecution(rs, t.Task)
}

// withExecutionBinding applies a task's binding to the config of a process of its own — a turn or the format repair
// of its answer: the model arguments after the stream-json defaults, the reasoning and the runtime context.
func withExecutionBinding(cfg executor.Config, b assistants.ExecutionBinding) executor.Config {
	cfg.ExtraArgs = nil
	if len(b.ExtraArgs) > 0 {
		cfg.ExtraArgs = executor.HelperClaudeArgs(b.ExtraArgs)
	}
	cfg.Reasoning = b.Reasoning
	cfg.HelperWrapperRuntime = b.RuntimeContext == assistants.RuntimeContextWrapper
	return cfg
}

// executorLabel names an executor for a status reason: its command and where that came from, never its argv.
func executorLabel(exec assistants.Execution) string {
	return fmt.Sprintf("command %s, source %s", exec.Command, exec.Binding.Source)
}

// executorChangedReason is why t must not start another turn, "" when it may: its command's wrapper is now built
// from another recipe than the one it was admitted with. A task without a binding is from an older journal and
// runs the current default executor, so there is nothing to compare.
func (o *Orchestrator) executorChangedReason(t assistants.Task, exec assistants.Execution) string {
	if t.Execution == nil || t.Execution.RecipeIdentity == o.recipeIdentity(exec.Command) {
		return ""
	}
	return o.helperTurnFailure(executorChangedPrefix+"the recipe changed after the task was created", t, exec,
		"attach a new helper to use the new settings")
}

// AssistantProfiles lists the configured profiles as they would run when attached to stageID, or as run agents when
// stageID is "": the executor each resolves to, its source, runtime context and whether this process may run it.
func (o *Orchestrator) AssistantProfiles(stageID string) ([]assistants.ProfileView, error) {
	if !o.opts.AssistantsEnabled || o.opts.AssistantsRegistry == nil {
		return nil, attachRejected(assistants.CodeFeatureDisabled, "collaboration is disabled")
	}
	if stageID != "" {
		st := o.reg().Stage(stageID)
		if st == nil {
			return nil, attachRejected(assistants.CodeNotFound, "unknown stage %q", stageID)
		}
		if st.IsScript() {
			return nil, attachRejected(assistants.CodeInvalidRequest, "stage %q is a script stage — helpers are not supported there", stageID)
		}
	}
	in := o.executionInputs(stageID, nil)
	profiles := o.opts.AssistantsRegistry.List()
	views := make([]assistants.ProfileView, 0, len(profiles))
	for _, p := range profiles {
		view := assistants.ProfileView{ID: p.ID, Title: p.Title}
		exec, err := assistants.ResolveExecution(p, in)
		if err != nil {
			view.UnavailableReason = err.Error()
			views = append(views, view)
			continue
		}
		rejection, enforcement := o.helperCommandAccess(exec.Command)
		view.Command, view.CommandSource, view.RuntimeContext = exec.Command, exec.Binding.Source, exec.Binding.RuntimeContext
		view.Available, view.UnavailableReason, view.ReadonlyEnforcement = rejection == "", rejection, enforcement
		views = append(views, view)
	}
	return views, nil
}
