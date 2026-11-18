package orchestrator

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// roundHelperReport is what one pre_completion helper said in a round, as the report and the author's feedback show it.
type roundHelperReport struct {
	Task       state.TaskState
	Outcome    state.RoundOutcome
	Result     *assistants.Result // nil when the helper gave no result for the round's request
	ResultPath string             // the full turn-N-result.json; "" without a result
}

func (h roundHelperReport) required() bool { return h.Task.Mode == assistants.ModeRequired }

// collectRoundReports reads each helper of the stage's open round in the order the tasks were created: its outcome
// for the round's request and, when it answered, the committed result bound to that request.
func (o *Orchestrator) collectRoundReports(rs state.RunState, stageID string) []roundHelperReport {
	p := rs.Stages[stageID].PreCompletion
	request := state.PreCompletionRequestID(p.RoundSeq)
	var out []roundHelperReport
	for _, id := range p.Tasks {
		t := rs.Assistants.Tasks[id]
		if t == nil {
			continue
		}
		_, outcome := state.PreCompletionTaskOutcome(rs, t)
		report := roundHelperReport{Task: *t, Outcome: outcome}
		if result, turn, ok := roundResultOf(o.opts.Store.TaskEvents(id), request); ok {
			report.Result = &result
			report.ResultPath = filepath.Join(o.taskDir(id), fmt.Sprintf("turn-%d-result.json", turn))
		}
		out = append(out, report)
	}
	return out
}

// roundResultOf finds the latest result bound to the round request in a task's journal.
func roundResultOf(events []state.TaskEvent, request agentcomm.MessageID) (assistants.Result, uint64, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != taskResultEffectKind {
			continue
		}
		var p state.TaskResultPayload
		if json.Unmarshal(events[i].Payload, &p) == nil && p.RequestID == string(request) {
			return p.Result, p.Turn, true
		}
	}
	return assistants.Result{}, 0, false
}

// preCompletionFeedbackHeading opens the block that carries the active feedback into the author's prompt.
const preCompletionFeedbackHeading = "## Замечания помощников pre-completion"

// renderPreCompletionFeedback is the active feedback of the author's correction turn: the required helpers that
// asked for changes, verbatim, and the advisory ones apart as non-blocking. "" when nobody has anything to say.
func renderPreCompletionFeedback(round int, roundSeq uint64, reportPath string, helpers []roundHelperReport) string {
	var blocking, advisory []roundHelperReport
	for _, h := range helpers {
		switch {
		case h.required() && h.Outcome == state.RoundNeedsChanges:
			blocking = append(blocking, h)
		case !h.required() && h.Result != nil && (h.Outcome != state.RoundPass || resultHasSubstance(*h.Result)):
			advisory = append(advisory, h)
		default:
		}
	}
	if len(blocking) == 0 && len(advisory) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(stagefiles.PreCompletionFeedbackMark(round, roundSeq))
	fmt.Fprintf(&b, "\n# Pre-completion: раунд %d\n\nПолный отчёт: %s\n\n", round, reportPath)
	if len(blocking) > 0 {
		b.WriteString("## Требуют исправления\n\n")
		for _, h := range blocking {
			writeHelperSection(&b, h)
		}
	}
	if len(advisory) > 0 {
		b.WriteString("## Рекомендации (не блокируют)\n\n")
		for _, h := range advisory {
			writeHelperSection(&b, h)
		}
	}
	return b.String()
}

// renderPreCompletionReport is the full record of a round: every helper with its outcome, whatever it answered.
func renderPreCompletionReport(stageID string, round int, roundSeq uint64, decision, reason string, helpers []roundHelperReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Pre-completion: стадия %s, раунд %d (seq %d)\n\nРешение: %s\n", stageID, round, roundSeq, decision)
	if reason != "" {
		fmt.Fprintf(&b, "Причина: %s\n", reason)
	}
	b.WriteString("\n")
	for _, h := range helpers {
		outcome := string(h.Outcome)
		if outcome == "" {
			outcome = "pending"
		}
		fmt.Fprintf(&b, "- %s (%s, %s): %s\n", h.Task.Title, h.Task.ID, h.Task.Mode, outcome)
	}
	b.WriteString("\n")
	for _, h := range helpers {
		writeHelperSection(&b, h)
	}
	return b.String()
}

// writeHelperSection writes one helper's answer verbatim: its summary, suggested change, findings, evidence and
// limitations, under its title and id, with the path of the full result.
func writeHelperSection(b *strings.Builder, h roundHelperReport) {
	mode := "advisory"
	if h.required() {
		mode = "required"
	}
	fmt.Fprintf(b, "### %s (%s, %s)\n\n", h.Task.Title, h.Task.ID, mode)
	if h.Result == nil {
		fmt.Fprintf(b, "Результата нет: %s.\n\n", describeNoResult(h.Outcome))
		return
	}
	fmt.Fprintf(b, "Вердикт: %s\nПолный результат: %s\n\n", verdictText(h.Result.Verdict), h.ResultPath)
	writeLabelled(b, "Итог", h.Result.Summary)
	writeLabelled(b, "Предлагаемое изменение", h.Result.SuggestedChange)
	if len(h.Result.Findings) > 0 {
		b.WriteString("Findings:\n")
		for _, f := range h.Result.Findings {
			fmt.Fprintf(b, "- [%s] %s: %s%s\n", f.Severity, f.ID, f.Detail, findingLocation(f))
		}
		b.WriteString("\n")
	}
	if len(h.Result.Evidence) > 0 {
		b.WriteString("Evidence:\n")
		for _, e := range h.Result.Evidence {
			fmt.Fprintf(b, "- %s\n", e)
		}
		b.WriteString("\n")
	}
	writeLabelled(b, "Ограничения", h.Result.Limitations)
}

// resultHasSubstance reports whether a passing answer still carries something the author should read.
func resultHasSubstance(r assistants.Result) bool {
	return len(r.Findings) > 0 || strings.TrimSpace(r.SuggestedChange) != ""
}

func writeLabelled(b *strings.Builder, label, text string) {
	if strings.TrimSpace(text) != "" {
		fmt.Fprintf(b, "%s:\n%s\n\n", label, text)
	}
}

func findingLocation(f assistants.Finding) string {
	if f.Path == nil {
		return ""
	}
	loc := *f.Path
	if f.LineStart != nil {
		loc += fmt.Sprintf(":%d", *f.LineStart)
		if f.LineEnd != nil && *f.LineEnd != *f.LineStart {
			loc += fmt.Sprintf("-%d", *f.LineEnd)
		}
	}
	return " (" + loc + ")"
}

func verdictText(v assistants.Verdict) string {
	if v == "" {
		return "не указан"
	}
	return string(v)
}

func describeNoResult(outcome state.RoundOutcome) string {
	switch outcome {
	case state.RoundWaived:
		return "раунд снят человеком (waive)"
	case state.RoundFailed:
		return "ход помощника завершился ошибкой"
	default:
		return "помощник ещё не ответил"
	}
}
