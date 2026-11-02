package prompts

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/schema"
)

// Agent — фаза выполнения (алиас доменного flow.Phase). Значения приходят из единого источника pkg/flow;
// локальные имена сохранены для читаемости вызовов prompts.AgentPlanning и т.п.
type Agent = flow.Phase

const (
	AgentPlanning       = flow.PhasePlanning
	AgentImplementation = flow.PhaseImplementation
	AgentReview         = flow.PhaseReview
	AgentAutonomous     = flow.PhaseAutonomous
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
	// Autonomous — шаблон для автономного трека (без plan.md, с execution_summary.md).
	// Если непустой, используется вместо Template.
	Autonomous string
	// MemoryBlock — по-стадийный срез agent-памяти, посчитанный вызывающей стороной под конкретную стадию —
	// либо inlined findings, либо pointer на файлы памяти.
	MemoryBlock string
	// BrowserRuntime, если non-nil, добавляет блок <browser_tools> — короткая подсказка агенту, что у него
	// есть managed-браузер и как им пользоваться.
	BrowserRuntime *BrowserRuntime
	// AgentComm is the pre-rendered <agent_communication> block (Agent Mesh Phase 4, T4.1/T4.2) — built by
	// AgentCommunicationBlock and passed in whole, exactly like CompletionContract is folded into
	// RetryContext: Build() appends it unconditionally, so a custom prompts_dir template can never make the
	// mesh contract silently disappear. Empty means "do not render the block at all" — every production call
	// site always sets it, even when mesh is unavailable, via AgentCommunicationBlock's own Available=false
	// branch; empty is only exercised by tests that build Inputs directly.
	AgentComm string
	// AgentCommInbox is the pre-rendered <inbox> block (T4.4) — built by AgentCommInboxBlock. Empty means no
	// messages were already addressed to this participant; nothing is rendered.
	AgentCommInbox string
	// SkillLibrary is the extra skill library catalog for the stage's <skills> block; the zero value omits it.
	SkillLibrary SkillLibrary
}

// BrowserRuntime describes the live managed-browser capability for the prompt.
type BrowserRuntime struct {
	AppURL     string
	AppPort    int
	ServerName string
	// Docker apps bind 0.0.0.0 for host browser access; native apps bind loopback.
	Docker bool
}

func Build(in Inputs) string {
	var sb strings.Builder

	// Если задан автономный шаблон — использовать его вместо основного.
	tmpl := in.Template
	if in.Autonomous != "" {
		tmpl = in.Autonomous
	}
	sb.WriteString("<system_rules>\n")
	sb.WriteString(tmpl)
	if in.OutputContractMD != "" {
		sb.WriteString("\n\n")
		sb.WriteString(in.OutputContractMD)
	}
	if in.Interactive {
		// Печатаем буквальный абсолютный путь stage-директории, чтобы у агента не было причин придумывать путь
		// (afm bug-2: агент писал question.json в .afm/stages/... и ломал диалог).
		stageDir := in.StageDir
		if abs, err := filepath.Abs(in.StageDir); err == nil && abs != "" {
			stageDir = abs
		}
		writeInteractiveRules(&sb, in, stageDir)
	}

	// Контракт публикации картинки в ленту: агент может показать пользователю файл-изображение, атомарно
	// опубликовав его как immutable-артефакт стадии и напечатав маркер отдельной строкой.
	sb.WriteString("\n\n<image_output>\n")
	sb.WriteString("To show the user an image you produced (a chart, rendered diagram, screenshot, or downloaded picture), publish it as a stage artifact and reference it in your narrative text:\n")
	sb.WriteString("1. Ensure the directory exists: $AFM_STAGE_DIR/artifacts/\n")
	sb.WriteString("2. Write the image ATOMICALLY: write the bytes to a TEMPORARY file in that same directory, flush/fsync it, then rename it to the final name. Never print the marker for a partially-written file.\n")
	sb.WriteString("   The final name must match [A-Za-z0-9._-]+ (no slashes, no \"..\") and end in .png, .jpg, .jpeg, or .gif.\n")
	sb.WriteString("3. ONLY AFTER the rename completes, print the marker on its OWN line, nothing else on that line: [AFM image: <name>]\n")
	sb.WriteString("   Example: [AFM image: chart.png]  (one marker per line; for several images print several lines).\n")
	sb.WriteString("Supported formats: PNG, JPEG, GIF. Do NOT print the marker before the file is fully written and renamed.\n")
	sb.WriteString("</image_output>\n")

	if in.BrowserRuntime != nil {
		b := in.BrowserRuntime
		sb.WriteString("\n\n<browser_tools>\n")
		fmt.Fprintf(&sb, "You have a managed Chrome DevTools browser via the MCP server %q — use it to run the app for real (a live run), not just unit tests.\n", b.ServerName)
		if b.Docker {
			fmt.Fprintf(&sb, "- Serve the app under test on 0.0.0.0:$AFM_BROWSER_APP_PORT (port %d) — bind 0.0.0.0 inside the container, not localhost, so the host browser can reach the published port.\n", b.AppPort)
		} else {
			fmt.Fprintf(&sb, "- Serve the app under test on 127.0.0.1:$AFM_BROWSER_APP_PORT (port %d).\n", b.AppPort)
		}
		fmt.Fprintf(&sb, "- Open the app at $AFM_BROWSER_APP_URL (%s). Always use this exact value — do not guess the URL.\n", b.AppURL)
		fmt.Fprintf(&sb, "- Drive the page with the %s MCP tools (navigate, snapshot, click, fill, evaluate, screenshot, console/network inspection).\n", b.ServerName)
		sb.WriteString("- Save screenshots / console logs / network captures into $AFM_STAGE_DIR/artifacts so they appear in the run.\n")
		sb.WriteString("- If the browser is unavailable or the scenario fails, treat it as a FAILED check — do not claim success.\n")
		sb.WriteString("</browser_tools>\n")
	}

	if in.AgentComm != "" {
		sb.WriteString(in.AgentComm)
	}
	if in.AgentCommInbox != "" {
		sb.WriteString(in.AgentCommInbox)
	}

	sb.WriteString("\n</system_rules>\n\n")

	if in.GlobalPrompt != "" {
		sb.WriteString("<global_prompt>\n")
		sb.WriteString(escapeTags(in.GlobalPrompt))
		sb.WriteString("\n</global_prompt>\n\n")
	}

	if in.MemoryBlock != "" {
		sb.WriteString("<project_memory>\n")
		sb.WriteString(escapeTags(in.MemoryBlock))
		sb.WriteString("\n</project_memory>\n\n")
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
	sb.WriteString(SkillsBlock(in.Stage.Skills, in.SkillLibrary))
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
	"</project_memory>", "</\u200bproject_memory>",
	"<project_memory>", "<\u200bproject_memory>",
	"</skills>", "</\u200bskills>",
	"<skills>", "<\u200bskills>",
	"</inbox>", "</\u200binbox>",
	"<inbox>", "<\u200binbox>",
	"</your_requests>", "</\u200byour_requests>",
	"<your_requests>", "<\u200byour_requests>",
	"</your_board_tasks>", "</\u200byour_board_tasks>",
	"<your_board_tasks>", "<\u200byour_board_tasks>",
	"</your_helpers>", "</\u200byour_helpers>",
	"<your_helpers>", "<\u200byour_helpers>",
	"</agent_communication>", "</\u200bagent_communication>",
	"<agent_communication>", "<\u200bagent_communication>",
)

// writeInteractiveRules explains the dialog: the afm commands are the main path (they validate and number the
// question themselves), hand-written files are only a fallback for an unavailable command.
func writeInteractiveRules(sb *strings.Builder, in Inputs, stageDir string) {
	sb.WriteString("\n\n<interactive_rules>\n")
	sb.WriteString("Ask the user questions with the two afm commands below. They pick the question id, write the file and check the question, so you never write dialog files yourself.\n")
	fmt.Fprintf(sb, "Your stage directory is AFM_STAGE_DIR=%q (the commands read it from the environment).\n\n", stageDir)
	sb.WriteString("1. Ask: pass the question text on stdin and one --option per answer option (the heredoc lines start at column 0):\n")
	sb.WriteString("afm stage ask --option \"A\" --option \"B\" <<'EOF'\n")
	sb.WriteString("## Full context here\n\nYour question?\n")
	sb.WriteString("EOF\n")
	sb.WriteString("   Put ALL context in the text: descriptions, trade-offs, examples. Use markdown freely.\n")
	sb.WriteString("   Options are optional; add --allow-custom=false only when the user must pick one of them.\n")
	sb.WriteString("   It prints {\"id\":\"qN\",\"wait\":\"afm stage wait-answer qN\"}. Asking the same question again returns the same id, so repeating the command is safe.\n")
	sb.WriteString("2. Wait: run the printed wait command with the Bash tool timeout set to at least 120 seconds (e.g. 150000 ms):\n")
	sb.WriteString("   afm stage wait-answer qN\n")
	sb.WriteString("   Exit 0 prints the answer as JSON {\"id\",\"answer\",\"from_options\"}: continue with it.\n")
	sb.WriteString("   Exit 75 prints {\"status\":\"waiting\"}: the user has not answered yet. This is EXPECTED and is NOT a signal to stop. Run the EXACT SAME command again, as many times as it takes.\n")
	sb.WriteString("3. A mistake in your call exits 1 with {\"error\",\"message\"} on stderr (message names the field). Fix the call as the message says and run it again. Do NOT switch to writing files by hand because of such an error.\n")
	sb.WriteString("   error question_pending means an earlier question is still open: wait for its answer first, then ask the next one.\n\n")
	sb.WriteString("Ask ONE question at a time and wait for its answer before the next. If you have several questions, ask them one by one (recommended) or combine them into a SINGLE question with numbered sub-questions.\n")
	sb.WriteString("You MUST keep waiting until the answer arrives. Do NOT end your turn, do NOT write \"I'll wait\" and return control.\n")
	sb.WriteString("Do NOT use ScheduleWakeup, background tasks, async waits, or \"wait for a notification\" — those mechanisms DO NOT EXIST here. The ONLY way to receive the user's answer is `afm stage wait-answer`.\n")
	sb.WriteString("Do NOT write to plan.md / output artifact yet — finish waiting for and processing all answers first, then produce the artifact in one go.\n\n")
	sb.WriteString("Fallback, ONLY if `afm stage ask` itself is unavailable (command not found, or it fails for a reason other than your call being invalid): write the files by hand.\n")
	fmt.Fprintf(sb, "  Question file: $AFM_STAGE_DIR/%s.q<N>.question.json, answer file: $AFM_STAGE_DIR/%s.q<N>.answer.json\n", in.PhaseAgent, in.PhaseAgent)
	fmt.Fprintf(sb, "  The prefix is EXACTLY %q — NOT the stage id %q. Ids are q1, q2, … (never reused); one open question at a time.\n", in.PhaseAgent, in.Stage.ID)
	fmt.Fprintf(sb, "  Wait with a blocking Bash loop and re-run it after a command timeout: while [ ! -f \"$AFM_STAGE_DIR/%s.qN.answer.json\" ]; do sleep 15; done && cat \"$AFM_STAGE_DIR/%s.qN.answer.json\"\n", in.PhaseAgent, in.PhaseAgent)
	sb.WriteString("  Question JSON Schema (the filename owns the question ID):\n")
	sb.WriteString(schema.JSON(schema.Question))
	sb.WriteString("\n</interactive_rules>\n")
}

func escapeTags(s string) string {
	return tagReplacer.Replace(s)
}

// EscapeTagsForReprompt exports escapeTags for use in re-prompt construction.
func EscapeTagsForReprompt(s string) string { return escapeTags(s) }
