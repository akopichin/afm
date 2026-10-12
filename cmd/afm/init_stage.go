package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
)

const (
	stageModeStandard = 0
	stageModeAuto     = 1
	stageModeScript   = 2
)

var stageModeOptions = []string{
	"Standard agent stage (planning/implementation/review — choose phases)",
	"auto (fully autonomous agent, no planning/supervisor)",
	"script (shell command instead of an agent)",
}

const (
	planModeAgent = 0
	planModeFile  = 1
)

var planModeOptions = []string{
	"Plan with an agent (this stage writes its own plan)",
	"I already have a plan file (I'll provide the path)",
}

// phaseNameImplementation/phaseNameReview name the two selectable phases in phaseOptions — pulled out as
// constants purely to satisfy goconst.
const (
	phaseNameImplementation = "implementation"
	phaseNameReview         = "review"
)

var phaseOptions = []string{phaseNameImplementation, phaseNameReview}

// stageDefaults configures the suggested defaults for one round of askStageDetails. DependsOn is used
// verbatim unless AskDeps is true, in which case it's read interactively instead.
type stageDefaults struct {
	FixedID       string
	SuggestedID   string
	SuggestedName string
	DefaultMode   int
	AskDeps       bool
	DependsOn     []string
	ForceVerify   bool
}

// askStageDetails runs the full per-stage question sequence and returns the resulting Stage.
func askStageDetails(scanner *bufio.Scanner, w io.Writer, d stageDefaults, priorStages []flow.Stage) flow.Stage {
	id := d.FixedID
	if id == "" {
		id = promptLine(scanner, w, fmt.Sprintf("Stage ID [%s]: ", d.SuggestedID))
		if id == "" {
			id = d.SuggestedID
		}
	}
	name := promptLine(scanner, w, fmt.Sprintf("Stage name [%s]: ", d.SuggestedName))
	if name == "" {
		name = d.SuggestedName
	}

	stage := flow.Stage{ID: id, Name: name}

	mode := promptChoice(scanner, w, "Stage mode:", stageModeOptions, d.DefaultMode)

	switch mode {
	case stageModeAuto:
		stage.Agents = []flow.AgentType{flow.AgentAuto}
	case stageModeScript:
		stage.Script = promptLine(scanner, w, "Shell command to run: ")
	default: // stageModeStandard
		stage.Description = promptLine(scanner, w, "Description: ")

		planMode := promptChoice(scanner, w, "Does this stage already have a plan?", planModeOptions, planModeAgent)
		if planMode == planModeFile {
			stage.Plan = promptLine(scanner, w, "Path to plan file: ")
		} else {
			stage.Agents = append(stage.Agents, flow.AgentPlanning)
		}

		selected := parsePhaseSelection(
			promptLine(scanner, w, "Which phases to include? [1] implementation [2] review (default: implementation): "),
			len(phaseOptions),
			[]int{0},
		)
		for _, idx := range selected {
			switch phaseOptions[idx] {
			case phaseNameImplementation:
				stage.Agents = append(stage.Agents, flow.AgentImplementation)
			case phaseNameReview:
				stage.Agents = append(stage.Agents, flow.AgentReview)
			default:
				// unreachable: idx is always within phaseOptions
			}
		}
	}

	if d.AskDeps {
		stage.DependsOn = askDependsOn(scanner, w, priorStages)
	} else {
		stage.DependsOn = d.DependsOn
	}

	if d.ForceVerify {
		stage.Verify = flow.NewShellVerify(promptLine(scanner, w, "Verify shell command (runs after the stage reports done; non-zero triggers one retry): "))
	}

	if promptYesNo(scanner, w, "Advanced settings for this stage? (artifacts/inputs/verify/interactive/custom command) [y/N]: ", false) {
		askAdvanced(scanner, w, &stage, priorStages)
	}

	return stage
}

// askDependsOn selects dependencies from prior stages to prevent unknown IDs and forward references.
func askDependsOn(scanner *bufio.Scanner, w io.Writer, priorStages []flow.Stage) []string {
	if len(priorStages) == 0 {
		return nil
	}
	ids := make([]string, len(priorStages))
	for i, s := range priorStages {
		ids[i] = s.ID
	}
	_, _ = fmt.Fprintln(w, "Depends on which existing stage(s)?")
	for i, id := range ids {
		_, _ = fmt.Fprintf(w, "  %d. %s\n", i+1, id)
	}
	raw := promptLine(scanner, w, "Comma-separated numbers, or empty for none: ")
	selected := parsePhaseSelection(raw, len(ids), nil)
	if len(selected) == 0 {
		return nil
	}
	deps := make([]string, len(selected))
	for i, idx := range selected {
		deps[i] = ids[idx]
	}
	return deps
}

// askAdvanced collects the optional advanced fields: artifacts, inputs, a verify command, interactive
// mode, and a custom agent command.
func askAdvanced(scanner *bufio.Scanner, w io.Writer, stage *flow.Stage, priorStages []flow.Stage) {
	for {
		name := promptLine(scanner, w, "  Artifact name (empty to stop): ")
		if name == "" {
			break
		}
		path := promptLine(scanner, w, "  Artifact path: ")
		desc := promptLine(scanner, w, "  Artifact description: ")
		stage.Artifacts = append(stage.Artifacts, flow.Artifact{Name: name, Path: path, Description: desc})
	}

	depsSet := make(map[string]bool, len(stage.DependsOn))
	for _, dep := range stage.DependsOn {
		depsSet[dep] = true
	}
	var candidates []string
	for _, prior := range priorStages {
		if !depsSet[prior.ID] {
			continue
		}
		for _, a := range prior.Artifacts {
			candidates = append(candidates, prior.ID+"."+a.Name)
		}
	}
	if len(candidates) > 0 {
		_, _ = fmt.Fprintf(w, "  Available inputs from dependencies: %s\n", strings.Join(candidates, ", "))
		chosen := promptLine(scanner, w, "  Which to consume as inputs? (comma-separated, empty for none): ")
		for _, c := range splitComma(chosen) {
			stage.Inputs = append(stage.Inputs, flow.Input{Ref: c})
		}
	}

	if stage.Verify.IsEmpty() {
		v := promptLine(scanner, w, "  Verify shell command (empty to skip): ")
		if v != "" {
			stage.Verify = flow.NewShellVerify(v)
		}
	}

	stage.Interactive = promptYesNo(scanner, w, "  Interactive (agent can ask the user questions)? [y/N]: ", false)

	cmd := promptLine(scanner, w, "  Custom agent command (empty for default): ")
	if cmd != "" {
		stage.Command = cmd
	}
}
