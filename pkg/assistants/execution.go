package assistants

import (
	"errors"
	"slices"
)

// CommandSource says where a task's executor command came from.
type CommandSource string

const (
	CommandSourceProfile CommandSource = "profile"
	CommandSourceStage   CommandSource = "stage"
	CommandSourceClient  CommandSource = "client"
	CommandSourceParent  CommandSource = "parent"
)

// ExecutionBinding is how a task's executor runs, fixed in its task_created record next to the resolved command
// (ProfileSnapshot.Command): Retry, Pause/Continue and a restart reuse it, so a config change reaches new tasks only.
// It holds no secret values and no generated paths.
type ExecutionBinding struct {
	Source CommandSource `json:"source"`
	// ExtraArgs are the allowlisted model arguments of client.extra_args: only the client source has them (and a
	// child that inherits them from its parent).
	ExtraArgs      []string       `json:"extra_args,omitempty"`
	Reasoning      string         `json:"reasoning,omitempty"`
	RuntimeContext RuntimeContext `json:"runtime_context,omitempty"`
	// RecipeIdentity is the digest of the command's docker.agents recipe in a re-exec'd autoShim container, "" without
	// a recipe or outside one. A different digest at a later turn means the alias now names another model or account.
	RecipeIdentity string `json:"recipe_identity,omitempty"`
}

// Clone returns an independent copy; nil stays nil.
func (b *ExecutionBinding) Clone() *ExecutionBinding {
	if b == nil {
		return nil
	}
	c := *b
	c.ExtraArgs = slices.Clone(b.ExtraArgs)
	return &c
}

// Execution is a task's resolved executor: the command and how it runs.
type Execution struct {
	Command string
	Binding ExecutionBinding
}

// ExecutionInputs are the executors a new task can inherit, gathered by the caller from the run.
type ExecutionInputs struct {
	// StageCommand is the root stage's own command: "" for a stage without one and for a run agent.
	StageCommand string
	// ClientCommand is client.command, ClientModelArgs the allowlisted model arguments of client.extra_args.
	ClientCommand   string
	ClientModelArgs []string
	ClientReasoning string
	// Parent is the executor of the delegating parent; nil for a top-level task.
	Parent *Execution
	// RuntimeContext is collaboration.runtime_context; nil means isolated.
	RuntimeContext *RuntimeContext
	// RecipeIdentity returns a command's recipe identity ("" without a recipe or outside an autoShim container);
	// nil means "" for every command.
	RecipeIdentity func(command string) string
}

// ErrNoExecutor means no layer names a command. Admission rejects the task instead of letting the executor fall
// back to a command nobody chose.
var ErrNoExecutor = errors.New("no executor command: the profile, its stage and client.command name none")

// ResolveExecution picks a new task's executor: its profile's command, else its parent's, its stage's or the client's.
// Like a stage agent, only an inherited executor keeps the reasoning and only the client's its model arguments; a
// child copies the parent's recipe identity, since it runs the very executor the parent was admitted with.
func ResolveExecution(profile Profile, in ExecutionInputs) (Execution, error) {
	exec := Execution{Binding: ExecutionBinding{RuntimeContext: ResolveRuntimeContext(profile.RuntimeContext, in.RuntimeContext)}}
	switch {
	case profile.Command != "":
		exec.Command = profile.Command
		exec.Binding.Source = CommandSourceProfile
	case in.Parent != nil:
		exec.Command = in.Parent.Command
		exec.Binding.Source = CommandSourceParent
		exec.Binding.ExtraArgs = slices.Clone(in.Parent.Binding.ExtraArgs)
		exec.Binding.Reasoning = in.Parent.Binding.Reasoning
		exec.Binding.RecipeIdentity = in.Parent.Binding.RecipeIdentity
	case in.StageCommand != "":
		exec.Command = in.StageCommand
		exec.Binding.Source = CommandSourceStage
		exec.Binding.Reasoning = in.ClientReasoning
	default:
		exec.Command = in.ClientCommand
		exec.Binding.Source = CommandSourceClient
		exec.Binding.ExtraArgs = slices.Clone(in.ClientModelArgs)
		exec.Binding.Reasoning = in.ClientReasoning
	}
	if exec.Command == "" {
		return Execution{}, ErrNoExecutor
	}
	if exec.Binding.Source != CommandSourceParent && in.RecipeIdentity != nil {
		exec.Binding.RecipeIdentity = in.RecipeIdentity(exec.Command)
	}
	return exec, nil
}
