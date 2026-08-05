package main

import (
	"bufio"
	"fmt"
	"io"

	"github.com/akopichin/afm/pkg/flow"
)

const (
	archetypeSingleChange   = 0
	archetypeVerifyLoop     = 1
	archetypeParallelTracks = 2
	archetypeCustom         = 3
)

var archetypeOptions = []string{
	"Single change (planning → implementation → review)",
	"Build + verify loop (build → automated check, one retry on failure)",
	"Parallel tracks → integration (independent stages merge into one)",
	"Custom (build stage-by-stage from scratch)",
}

// buildArchetypeStages dispatches to the stage-graph builder for the
// chosen archetype.
func buildArchetypeStages(archetype int, scanner *bufio.Scanner, w io.Writer) []flow.Stage {
	switch archetype {
	case archetypeVerifyLoop:
		return buildVerifyLoopStages(scanner, w)
	case archetypeParallelTracks:
		return buildParallelTracksStages(scanner, w)
	case archetypeCustom:
		return buildCustomStages(scanner, w)
	default: // archetypeSingleChange
		return buildSingleChangeStages(scanner, w)
	}
}

func buildSingleChangeStages(scanner *bufio.Scanner, w io.Writer) []flow.Stage {
	s := askStageDetails(scanner, w, stageDefaults{
		SuggestedID:   "implementation",
		SuggestedName: "Implementation",
		DefaultMode:   stageModeStandard,
	}, nil)
	return []flow.Stage{s}
}

func buildVerifyLoopStages(scanner *bufio.Scanner, w io.Writer) []flow.Stage {
	_, _ = fmt.Fprintln(w, "\nThis is one automatic retry on failure, not a full cycle — a real review-loop (goto) is still in development.")
	build := askStageDetails(scanner, w, stageDefaults{
		SuggestedID:   "build",
		SuggestedName: "Build",
		DefaultMode:   stageModeStandard,
		ForceVerify:   true,
	}, nil)
	check := askStageDetails(scanner, w, stageDefaults{
		SuggestedID:   "check",
		SuggestedName: "Check",
		DefaultMode:   stageModeScript,
		DependsOn:     []string{build.ID},
	}, []flow.Stage{build})
	return []flow.Stage{build, check}
}

func buildParallelTracksStages(scanner *bufio.Scanner, w io.Writer) []flow.Stage {
	n := promptInt(scanner, w, "How many parallel tracks? [2]: ", 2)
	tracks := make([]flow.Stage, 0, n)
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("track-%d", i)
		s := askStageDetails(scanner, w, stageDefaults{
			SuggestedID:   id,
			SuggestedName: fmt.Sprintf("Track %d", i),
			DefaultMode:   stageModeStandard,
		}, nil)
		tracks = append(tracks, s)
	}
	trackIDs := make([]string, len(tracks))
	for i, tr := range tracks {
		trackIDs[i] = tr.ID
	}
	integration := askStageDetails(scanner, w, stageDefaults{
		SuggestedID:   "integration",
		SuggestedName: "Integration",
		DefaultMode:   stageModeStandard,
		DependsOn:     trackIDs,
	}, tracks)
	return append(tracks, integration)
}

func buildCustomStages(scanner *bufio.Scanner, w io.Writer) []flow.Stage {
	var stages []flow.Stage
	for {
		_, _ = fmt.Fprintln(w, "\nAdd a stage (leave ID empty to finish):")
		id := promptLine(scanner, w, "  Stage ID: ")
		if id == "" {
			break
		}
		s := askStageDetails(scanner, w, stageDefaults{
			FixedID:       id,
			SuggestedName: id,
			DefaultMode:   stageModeStandard,
			AskDeps:       true,
		}, stages)
		stages = append(stages, s)
	}
	return stages
}
