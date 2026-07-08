package prompts

import (
	"fmt"
	"path/filepath"
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
	GlobalPrompt     string
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
		// Печатаем буквальный абсолютный путь stage-директории, чтобы у агента
		// не было причин придумывать путь (afm bug-2: агент писал question.json
		// в .afm/stages/... и ломал диалог).
		stageDir := in.StageDir
		if abs, err := filepath.Abs(in.StageDir); err == nil && abs != "" {
			stageDir = abs
		}
		sb.WriteString("\n\n<interactive_rules>\n")
		sb.WriteString("Use the file-based dialog protocol to ask the user questions.\n")
		fmt.Fprintf(&sb, "Your stage directory is AFM_STAGE_DIR=%q\n", stageDir)
		sb.WriteString("Assign sequential IDs: q1, q2, … (never reuse an ID within a phase).\n\n")
		sb.WriteString("For each question:\n")
		sb.WriteString("1. Write the question file using the Write tool:\n")
		fmt.Fprintf(&sb, "   Path: %s/%s.q<N>.question.json  (== $AFM_STAGE_DIR/%s.q<N>.question.json)\n", stageDir, in.PhaseAgent, in.PhaseAgent)
		sb.WriteString("   Write the file to this path and NOWHERE ELSE. Do NOT invent paths like .afm/stages/... — always use $AFM_STAGE_DIR.\n")
		sb.WriteString("   Content: {\"id\":\"qN\",\"question\":\"## Full context here\\n\\nYour question?\",\"options\":[\"A\",\"B\"],\"allow_custom\":true}\n")
		sb.WriteString("   Put ALL context in 'question': descriptions, trade-offs, examples. Use markdown freely.\n")
		sb.WriteString("2. Wait for the answer via a blocking Bash polling loop:\n")
		fmt.Fprintf(&sb, "   while [ ! -f \"$AFM_STAGE_DIR/%s.qN.answer.json\" ]; do sleep 15; done && cat \"$AFM_STAGE_DIR/%s.qN.answer.json\"\n", in.PhaseAgent, in.PhaseAgent)
		sb.WriteString("3. The polling loop may be cut short by a command timeout after a few minutes — this is EXPECTED and is NOT a signal to stop.\n")
		sb.WriteString("   When that happens, immediately run the EXACT SAME polling-loop command again. Keep re-launching it until the answer file appears.\n")
		sb.WriteString("4. You MUST keep waiting until $AFM_STAGE_DIR/<phase>.qN.answer.json exists. Do NOT end your turn, do NOT stop, do NOT write \"I'll wait\" and return control.\n")
		sb.WriteString("5. Do NOT use ScheduleWakeup, background tasks, async waits, or \"wait for a notification\" — those mechanisms DO NOT EXIST here. The ONLY way to receive the user's answer is the blocking Bash polling loop above.\n")
		sb.WriteString("6. Do NOT write to plan.md / output artifact yet — finish waiting for and processing all answers first, then produce the artifact in one go.\n")
		sb.WriteString("Ask ONE question at a time.\n")
		sb.WriteString("</interactive_rules>\n")
	}
	sb.WriteString("\n</system_rules>\n\n")

	if in.GlobalPrompt != "" {
		sb.WriteString("<global_prompt>\n")
		sb.WriteString(escapeTags(in.GlobalPrompt))
		sb.WriteString("\n</global_prompt>\n\n")
	}

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

	// Explicit per-stage directive, escaped like Description.
	if in.Stage.Prompt != "" {
		sb.WriteString("\n<prompt>\n")
		sb.WriteString(escapeTags(in.Stage.Prompt))
		sb.WriteString("\n</prompt>\n")
	}

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
	"</prompt>", "</\u200bprompt>",
	"<prompt>", "<\u200bprompt>",
	"</global_prompt>", "</\u200bglobal_prompt>",
	"<global_prompt>", "<\u200bglobal_prompt>",
)

func escapeTags(s string) string {
	return tagReplacer.Replace(s)
}

// EscapeTagsForReprompt exports escapeTags for use in re-prompt construction.
func EscapeTagsForReprompt(s string) string { return escapeTags(s) }
