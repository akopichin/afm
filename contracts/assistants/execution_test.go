package assistants_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/akopichin/afm/contracts/assistants"
)

func runtimePtr(r assistants.RuntimeContext) *assistants.RuntimeContext { return &r }

// identityOf is a recipe-identity lookup for the generated wrappers of "recipe-a" and "recipe-b" only.
func identityOf(cmd string) string {
	switch cmd {
	case "recipe-a":
		return "id-a"
	case "recipe-b":
		return "id-b"
	default:
		return ""
	}
}

func clientInputs() assistants.ExecutionInputs {
	return assistants.ExecutionInputs{
		ClientCommand:   "claude",
		ClientModelArgs: []string{"--model", "client-model"},
		ClientReasoning: "high",
		RecipeIdentity:  identityOf,
	}
}

func TestResolveExecution_Matrix(t *testing.T) {
	roleOnly := assistants.Profile{ID: "reviewer", Instructions: "review"}
	explicit := assistants.Profile{ID: "external", Command: "recipe-a"}
	inheritedParent := assistants.Execution{Command: "claude", Binding: assistants.ExecutionBinding{
		Source: assistants.CommandSourceClient, ExtraArgs: []string{"--model", "client-model"}, Reasoning: "high",
		RuntimeContext: assistants.RuntimeContextWrapper,
	}}
	explicitParent := assistants.Execution{Command: "recipe-b", Binding: assistants.ExecutionBinding{
		Source: assistants.CommandSourceProfile, RuntimeContext: assistants.RuntimeContextIsolated, RecipeIdentity: "id-b-old",
	}}

	tests := []struct {
		name    string
		profile assistants.Profile
		in      func() assistants.ExecutionInputs
		want    assistants.Execution
	}{
		{
			name: "stage helper, role-only profile on a stage with its own command inherits the stage's",
			in: func() assistants.ExecutionInputs {
				in := clientInputs()
				in.StageCommand = "recipe-b"
				return in
			},
			profile: roleOnly,
			want: assistants.Execution{Command: "recipe-b", Binding: assistants.ExecutionBinding{
				Source: assistants.CommandSourceStage, Reasoning: "high",
				RuntimeContext: assistants.RuntimeContextIsolated, RecipeIdentity: "id-b",
			}},
		},
		{
			name:    "stage helper on a stage without a command, and a run agent, inherit the client with its model args",
			in:      clientInputs,
			profile: roleOnly,
			want: assistants.Execution{Command: "claude", Binding: assistants.ExecutionBinding{
				Source: assistants.CommandSourceClient, ExtraArgs: []string{"--model", "client-model"}, Reasoning: "high",
				RuntimeContext: assistants.RuntimeContextIsolated,
			}},
		},
		{
			name: "an explicit profile command wins over the stage and gets neither client args nor reasoning",
			in: func() assistants.ExecutionInputs {
				in := clientInputs()
				in.StageCommand = "recipe-b"
				return in
			},
			profile: explicit,
			want: assistants.Execution{Command: "recipe-a", Binding: assistants.ExecutionBinding{
				Source: assistants.CommandSourceProfile, RuntimeContext: assistants.RuntimeContextIsolated, RecipeIdentity: "id-a",
			}},
		},
		{
			name: "a child of an inheriting parent copies the parent's executor, args and reasoning",
			in: func() assistants.ExecutionInputs {
				in := clientInputs()
				in.Parent = &inheritedParent
				return in
			},
			profile: roleOnly,
			want: assistants.Execution{Command: "claude", Binding: assistants.ExecutionBinding{
				Source: assistants.CommandSourceParent, ExtraArgs: []string{"--model", "client-model"}, Reasoning: "high",
				RuntimeContext: assistants.RuntimeContextIsolated,
			}},
		},
		{
			name: "a child of an explicit parent copies its command and the recipe identity the parent was admitted with",
			in: func() assistants.ExecutionInputs {
				in := clientInputs()
				in.Parent = &explicitParent
				return in
			},
			profile: roleOnly,
			want: assistants.Execution{Command: "recipe-b", Binding: assistants.ExecutionBinding{
				Source: assistants.CommandSourceParent, RuntimeContext: assistants.RuntimeContextIsolated, RecipeIdentity: "id-b-old",
			}},
		},
		{
			name: "a child with an explicit profile command gets the current recipe identity, not the parent's",
			in: func() assistants.ExecutionInputs {
				in := clientInputs()
				in.Parent = &explicitParent
				return in
			},
			profile: assistants.Profile{ID: "child", Command: "recipe-b"},
			want: assistants.Execution{Command: "recipe-b", Binding: assistants.ExecutionBinding{
				Source: assistants.CommandSourceProfile, RuntimeContext: assistants.RuntimeContextIsolated, RecipeIdentity: "id-b",
			}},
		},
		{
			name: "runtime context: collaboration default applies to a profile without its own",
			in: func() assistants.ExecutionInputs {
				in := clientInputs()
				in.RuntimeContext = runtimePtr(assistants.RuntimeContextWrapper)
				return in
			},
			profile: roleOnly,
			want: assistants.Execution{Command: "claude", Binding: assistants.ExecutionBinding{
				Source: assistants.CommandSourceClient, ExtraArgs: []string{"--model", "client-model"}, Reasoning: "high",
				RuntimeContext: assistants.RuntimeContextWrapper,
			}},
		},
		{
			name: "runtime context: the profile's own wins over the collaboration default",
			in: func() assistants.ExecutionInputs {
				in := clientInputs()
				in.RuntimeContext = runtimePtr(assistants.RuntimeContextWrapper)
				return in
			},
			profile: assistants.Profile{ID: "x", Command: "/abs/ext-as-claude.sh", RuntimeContext: runtimePtr(assistants.RuntimeContextIsolated)},
			want: assistants.Execution{Command: "/abs/ext-as-claude.sh", Binding: assistants.ExecutionBinding{
				Source: assistants.CommandSourceProfile, RuntimeContext: assistants.RuntimeContextIsolated,
			}},
		},
		{
			name: "no recipe-identity lookup means no generated wrapper",
			in: func() assistants.ExecutionInputs {
				in := clientInputs()
				in.RecipeIdentity = nil
				return in
			},
			profile: explicit,
			want: assistants.Execution{Command: "recipe-a", Binding: assistants.ExecutionBinding{
				Source: assistants.CommandSourceProfile, RuntimeContext: assistants.RuntimeContextIsolated,
			}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := assistants.ResolveExecution(tc.profile, tc.in())
			if err != nil {
				t.Fatalf("ResolveExecution: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ResolveExecution =\n %+v\nwant\n %+v", got, tc.want)
			}
		})
	}
}

// The resolved binding owns its arguments: changing the inputs afterwards must not reach a task's record.
func TestResolveExecution_CopiesArguments(t *testing.T) {
	in := clientInputs()
	got, err := assistants.ResolveExecution(assistants.Profile{ID: "r"}, in)
	if err != nil {
		t.Fatal(err)
	}
	in.ClientModelArgs[1] = "mutated"
	if got.Binding.ExtraArgs[1] != "client-model" {
		t.Fatalf("binding args alias the input slice: %v", got.Binding.ExtraArgs)
	}

	parent := assistants.Execution{Command: "claude", Binding: assistants.ExecutionBinding{ExtraArgs: []string{"--model", "m"}}}
	child, err := assistants.ResolveExecution(assistants.Profile{ID: "r"}, assistants.ExecutionInputs{Parent: &parent})
	if err != nil {
		t.Fatal(err)
	}
	parent.Binding.ExtraArgs[1] = "mutated"
	if child.Binding.ExtraArgs[1] != "m" {
		t.Fatalf("child args alias the parent's: %v", child.Binding.ExtraArgs)
	}
}

// An empty result never reaches the executor's own "claude" fallback: it is a typed admission error.
func TestResolveExecution_EmptyInputsRejected(t *testing.T) {
	for name, in := range map[string]assistants.ExecutionInputs{
		"no layer":                 {},
		"parent without a command": {Parent: &assistants.Execution{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := assistants.ResolveExecution(assistants.Profile{ID: "r"}, in); !errors.Is(err, assistants.ErrNoExecutor) {
				t.Fatalf("err = %v, want ErrNoExecutor", err)
			}
		})
	}
}

func TestExecutionBinding_CloneIsDeep(t *testing.T) {
	var nilBinding *assistants.ExecutionBinding
	if nilBinding.Clone() != nil {
		t.Fatal("Clone of nil must stay nil")
	}
	b := &assistants.ExecutionBinding{Source: assistants.CommandSourceClient, ExtraArgs: []string{"--model", "m"}}
	c := b.Clone()
	c.ExtraArgs[1] = "changed"
	if b.ExtraArgs[1] != "m" {
		t.Fatalf("Clone shares ExtraArgs: %v", b.ExtraArgs)
	}
	if !reflect.DeepEqual(*b.Clone(), *b) {
		t.Fatal("Clone must equal the original")
	}
}
