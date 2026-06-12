package prompts

import (
	"fmt"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
)

type Agent string

const (
	AgentPlanning       Agent = "planning"
	AgentImplementation Agent = "implementation"
	AgentReview         Agent = "review"
)

type Inputs struct {
	Template         string
	Stage            flow.Stage
	PhaseAgent       Agent
	DependencyPlans  string
	Artifacts        string
	Plan             string
	PreviousPlan     string
	Feedback         string
	RetryContext     string
	StageDir         string
	Interactive      bool
	OutputContractMD string
	ExampleOutput    string
}

func Build(in Inputs) string {
	var sb strings.Builder

	sb.WriteString("<system_rules>\n")
	sb.WriteString(in.Template)
	if in.OutputContractMD != "" {
		sb.WriteString("\n\n")
		sb.WriteString(in.OutputContractMD)
	}
	if in.Interactive {
		sb.WriteString("\n\n<interactive_rules>\n")
		sb.WriteString("You may use the mcp__flowmanager__ask_user tool. Ask ONE question at a time. The tool BLOCKS until the user answers — wait, do not retry, do not skip.\n")
		sb.WriteString("</interactive_rules>\n")
	}
	sb.WriteString("\n</system_rules>\n\n")

	if in.DependencyPlans != "" || in.Artifacts != "" {
		sb.WriteString("<context>\n")
		if in.DependencyPlans != "" {
			sb.WriteString("<dependency_plans>\n")
			sb.WriteString(in.DependencyPlans)
			sb.WriteString("\n</dependency_plans>\n")
		}
		if in.Artifacts != "" {
			sb.WriteString("<artifacts>\n")
			sb.WriteString(in.Artifacts)
			sb.WriteString("\n</artifacts>\n")
		}
		sb.WriteString("</context>\n\n")
	}

	fmt.Fprintf(&sb, "<stage id=%q name=%q>\n", in.Stage.ID, in.Stage.Name)
	sb.WriteString("<description>\n")
	sb.WriteString(escapeTags(in.Stage.Description))
	sb.WriteString("\n</description>\n")
	if len(in.Stage.Skills) > 0 {
		fmt.Fprintf(&sb, "<skills>%s</skills>\n", strings.Join(in.Stage.Skills, ", "))
	}
	sb.WriteString("</stage>\n")

	if in.Plan != "" {
		sb.WriteString("\n<plan>\n")
		sb.WriteString(escapeTags(in.Plan))
		sb.WriteString("\n</plan>\n")
	}
	if in.PreviousPlan != "" {
		sb.WriteString("\n<previous_plan>\n")
		sb.WriteString(escapeTags(in.PreviousPlan))
		sb.WriteString("\n</previous_plan>\n")
	}
	if in.Feedback != "" {
		sb.WriteString("\n<feedback>\n")
		sb.WriteString(escapeTags(in.Feedback))
		sb.WriteString("\n</feedback>\n")
	}
	if in.RetryContext != "" {
		sb.WriteString("\n")
		sb.WriteString(in.RetryContext)
	}
	if in.ExampleOutput != "" {
		sb.WriteString("\n<example_output>\n")
		sb.WriteString(escapeTags(in.ExampleOutput))
		sb.WriteString("\n</example_output>\n")
	}
	return sb.String()
}

var tagReplacer = strings.NewReplacer(
	"</system_rules>", "</\u200bsystem_rules>",
	"<system_rules>", "<\u200bsystem_rules>",
	"</stage>", "</\u200bstage>",
	"<stage>", "<\u200bstage>",
	"</context>", "</\u200bcontext>",
	"<context>", "<\u200bcontext>",
	"</plan>", "</\u200bplan>",
	"<plan>", "<\u200bplan>",
	"</previous_plan>", "</\u200bprevious_plan>",
	"<previous_plan>", "<\u200bprevious_plan>",
	"</feedback>", "</\u200bfeedback>",
	"<feedback>", "<\u200bfeedback>",
	"</dependency_plans>", "</\u200bdependency_plans>",
	"<dependency_plans>", "<\u200bdependency_plans>",
	"</artifacts>", "</\u200bartifacts>",
	"<artifacts>", "<\u200bartifacts>",
	"</example_output>", "</\u200bexample_output>",
	"<example_output>", "<\u200bexample_output>",
	"</interactive_rules>", "</\u200binteractive_rules>",
	"<interactive_rules>", "<\u200binteractive_rules>",
)

func escapeTags(s string) string {
	return tagReplacer.Replace(s)
}

// EscapeTagsForReprompt exports escapeTags for use in re-prompt construction.
func EscapeTagsForReprompt(s string) string { return escapeTags(s) }
