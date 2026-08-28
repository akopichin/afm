package orchestrator

import (
	"fmt"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memory"
)

// memoryBlockForStage computes the per-stage memory slice to splice into the
// agent's prompt (prompts.Inputs.MemoryBlock) — replaces v1's static
// GlobalPrompt pointer, which was the SAME text for every stage of the flow
// regardless of relevance. Recomputed on every prompts.Build call (planning,
// implementation, review, autonomous and their *WithFeedback variants — see
// agents.go), so each stage sees a slice tailored to its own id/name/description.
//
// "" is a valid, expected result: memory disabled, or both stores genuinely
// have nothing to say (first stage of the first run, before any reflection
// wrote a finding) — in that case there is nothing worth pointing at either.
func (o *Orchestrator) memoryBlockForStage(s flow.Stage) string {
	if o.opts.MemoryProjectPath == "" {
		return ""
	}

	proj, _ := memory.Load(o.opts.MemoryProjectPath)
	sess, _ := memory.Load(o.opts.MemorySessionPath)
	if len(proj.Findings) == 0 && len(sess.Findings) == 0 {
		return ""
	}

	tokens := memory.Tokenize(s.ID + " " + s.Name + " " + s.Description)
	sel, all := memory.Select(proj, sess, tokens, memory.RetrievalConfig{
		Threshold:        o.opts.Memory.RetrievalThreshold,
		CoreConfirmCount: o.opts.Memory.CoreConfirmCount,
	})

	if all {
		return memoryPointerBlock(o.opts.MemoryProjectPath, o.opts.MemorySessionPath)
	}

	rendered := memory.Render(sel)
	pointerLine := fmt.Sprintf("More project memory in %s — read it if relevant.", o.opts.MemoryProjectPath)
	if rendered == "" {
		// Select filtered everything out for this stage (no core/relevant
		// findings), but the store isn't empty — still tell the agent where
		// to look, never return "" while memory is enabled and non-empty.
		return pointerLine
	}
	return rendered + "\n" + pointerLine
}

// memoryPointerBlock reproduces the v1 pointer wording (moved here from the
// removed cmd/afm.buildMemoryPointer, see Task 10): the agent reads the two
// files itself via its own Read tool rather than the prompt growing with the
// memory store.
func memoryPointerBlock(projectPath, sessionPath string) string {
	return fmt.Sprintf(`Project memory — accumulated findings from earlier stages and runs — lives at:
  %s
Session memory — this run's short-term context — lives at:
  %s
Before you start, read both files: each is a YAML list of findings, and each finding's "kind" field is one of fact, best_practice, or anti_pattern — take the best_practice and anti_pattern findings into account. They may not exist yet on the first stage; that is fine.`, projectPath, sessionPath)
}
