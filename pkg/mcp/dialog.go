// Package mcp implements the MCP (Model Context Protocol) HTTP server
// for the afm ask_user tool, plus per-phase dialog persistence.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kaptinlin/jsonrepair"

	"github.com/akopichin/afm/pkg/flow"
)

// Question is the record written when an agent calls ask_user.
type Question struct {
	ID          string   `json:"id"`
	TS          string   `json:"ts"`
	Question    string   `json:"question"`
	Options     []string `json:"options,omitempty"`
	AllowCustom bool     `json:"allow_custom"`
}

// Answer is the record written when the user replies.
type Answer struct {
	ID           string `json:"id"`
	TS           string `json:"ts"`
	Answer       string `json:"answer"`
	FromOptions  bool   `json:"from_options"`
	AutoAnswered bool   `json:"auto_answered,omitempty"`
}

// Entry is a grouped Q/A pair for reading. Answer is nil when the question
// is still open.
type Entry struct {
	ID           string
	TS           string
	Question     string
	Options      []string
	AllowCustom  bool
	Answer       *string
	AnswerTS     string
	FromOptions  bool
	AutoAnswered bool
}

// AppendQuestion writes a question record as a single JSON line.
func AppendQuestion(path string, q Question) error {
	if q.TS == "" {
		q.TS = time.Now().UTC().Format(time.RFC3339)
	}
	return appendLine(path, q)
}

// AppendAnswer writes an answer record as a single JSON line.
func AppendAnswer(path string, a Answer) error {
	if a.TS == "" {
		a.TS = time.Now().UTC().Format(time.RFC3339)
	}
	return appendLine(path, a)
}

// ReadDialog reads all records and groups them by ID into Entries in
// chronological order of the first question.
func ReadDialog(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open dialog: %w", err)
	}
	defer f.Close()

	byID := map[string]*Entry{}
	var order []string

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var probe struct {
			ID     string  `json:"id"`
			Answer *string `json:"answer"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			continue
		}
		if probe.Answer != nil {
			var a Answer
			if err := json.Unmarshal([]byte(line), &a); err != nil {
				continue
			}
			e, ok := byID[a.ID]
			if !ok {
				e = &Entry{ID: a.ID}
				byID[a.ID] = e
				order = append(order, a.ID)
			}
			ans := a.Answer
			e.Answer = &ans
			e.AnswerTS = a.TS
			e.FromOptions = a.FromOptions
			e.AutoAnswered = a.AutoAnswered
		} else {
			var q Question
			if err := json.Unmarshal([]byte(line), &q); err != nil {
				continue
			}
			if e, ok := byID[q.ID]; ok {
				// The answer line arrived before the question line (the two
				// records are written by different goroutines). Fill in the
				// question fields on the existing entry instead of dropping
				// them, leaving the answer fields untouched.
				e.TS = q.TS
				e.Question = q.Question
				e.Options = q.Options
				e.AllowCustom = q.AllowCustom
			} else {
				byID[q.ID] = &Entry{
					ID: q.ID, TS: q.TS, Question: q.Question,
					Options: q.Options, AllowCustom: q.AllowCustom,
				}
				order = append(order, q.ID)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan dialog: %w", err)
	}

	out := make([]Entry, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// FindEntry returns the entry with the given id, or nil if not present.
func FindEntry(path, id string) (*Entry, error) {
	entries, err := ReadDialog(path)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].ID == id {
			return &entries[i], nil
		}
	}
	return nil, nil
}

// appendMu serializes concurrent writers of dialog.jsonl. Two goroutines now
// append to the same files — the question poller (AppendQuestion) and the HTTP
// answer handler (AppendAnswer) — and O_APPEND does not guarantee byte-atomic
// writes for records larger than PIPE_BUF, so without a lock their bytes could
// interleave and corrupt the JSONL (silently dropped on read).
var appendMu sync.Mutex

// appendLine opens the file with O_APPEND and writes one JSON record + \n.
func appendLine(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')
	if len(data) > 1<<20 {
		return fmt.Errorf("dialog record too large (%d bytes > 1 MB)", len(data))
	}
	appendMu.Lock()
	defer appendMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open append: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// QuestionFile holds metadata extracted from a *.question.json file.
type QuestionFile struct {
	Phase       string
	ID          string
	Question    string
	Options     []string
	AllowCustom bool
	// Malformed is true when the file's JSON could not be parsed even after
	// jsonrepair. Callers that surface questions directly to a human (the
	// question poller, the dialog HTTP handler) must NOT show these to the
	// user immediately — see pkg/orchestrator/dialog_poller.go's retry state
	// machine, which decides whether this is a transient torn read (the
	// agent's Write tool hadn't finished landing on disk when this file was
	// scanned) or a genuine, persistent mistake worth nudging the agent
	// about. Callers that only care about "is something unanswered" (e.g.
	// hasOpenQuestion) can ignore this field — a malformed file still counts
	// as open.
	Malformed bool
}

// FindUnansweredQuestions scans stageDir for *.question.json files that do not
// have a matching *.answer.json. Filenames must follow "<phase>.<id>.question.json".
func FindUnansweredQuestions(stageDir string) ([]QuestionFile, error) {
	matches, err := filepath.Glob(filepath.Join(stageDir, "*.question.json"))
	if err != nil {
		return nil, err
	}
	var out []QuestionFile
	for _, qPath := range matches {
		base := strings.TrimSuffix(filepath.Base(qPath), ".question.json")
		dot := strings.Index(base, ".")
		if dot < 0 {
			continue
		}
		phase, id := base[:dot], base[dot+1:]

		if !flow.IsValidPhase(phase) {
			continue
		}

		answerPath := strings.TrimSuffix(qPath, ".question.json") + ".answer.json"
		if _, statErr := os.Stat(answerPath); statErr == nil {
			continue // already answered
		}

		raw, readErr := os.ReadFile(qPath)
		if readErr != nil {
			continue
		}
		var qf struct {
			ID          string   `json:"id"`
			Question    string   `json:"question"`
			Options     []string `json:"options"`
			AllowCustom *bool    `json:"allow_custom"`
		}
		malformed := false
		if err := json.Unmarshal(raw, &qf); err != nil {
			// Агент пишет question.json руками (файловый протокол диалога) и
			// иногда ломает JSON: незаэкранированная кавычка внутри строки,
			// пропущенная кавычка у ключа, пропущенная запятая и т.п.
			// jsonrepair покрывает эти классы ошибок гораздо шире одной
			// самописной эвристики.
			repaired, repairErr := jsonrepair.Repair(string(raw))
			if repairErr == nil {
				repairErr = json.Unmarshal([]byte(repaired), &qf)
			}
			if repairErr != nil {
				// Даже jsonrepair не справился. НЕ пишем ничего на диск здесь:
				// файл может быть просто недописан агентом (torn read —
				// поллер читает файл, пока Write ещё не долетел до диска), и
				// перезапись содержимого afm'ом гонится со всё ещё
				// выполняющейся записью агента. Решение "это временная гонка
				// или агент реально сломал JSON" принимает вызывающий
				// (pollQuestions'ный retry-автомат в dialog_poller.go) —
				// здесь только сигнализируем Malformed и отдаём то, что есть.
				log.Printf("WARN: %s: invalid JSON even after repair: %v", qPath, err)
				malformed = true
				qf.ID = id
				qf.Question = string(raw)
			} else if writeErr := os.WriteFile(qPath, []byte(repaired), 0644); writeErr != nil {
				log.Printf("WARN: %s: repaired invalid JSON in memory but failed to persist fix: %v", qPath, writeErr)
			} else {
				log.Printf("INFO: %s: repaired invalid JSON: %v", qPath, err)
			}
		}
		actualID := qf.ID
		if actualID == "" {
			actualID = id
		}
		if actualID == "" {
			continue
		}
		allowCustom := true
		if qf.AllowCustom != nil {
			allowCustom = *qf.AllowCustom
		}
		out = append(out, QuestionFile{
			Phase: phase, ID: actualID, Question: qf.Question,
			Options: qf.Options, AllowCustom: allowCustom, Malformed: malformed,
		})
	}
	return out, nil
}

// CanParseQuestion reports whether raw is a *.question.json payload that
// FindUnansweredQuestions would treat as parseable — a direct json.Unmarshal,
// or (failing that) after jsonrepair.Repair. Does not persist anything to
// disk; a caller that needs the repaired bytes should call
// FindUnansweredQuestions itself, which does both the check and the persist.
// Exposed for the orchestrator's malformed-question retry state machine
// (pkg/orchestrator/dialog_poller.go), which needs to know whether an
// agent's rewrite actually fixed a file without re-scanning the whole stage
// directory.
func CanParseQuestion(raw []byte) bool {
	var probe map[string]any
	if json.Unmarshal(raw, &probe) == nil {
		return true
	}
	repaired, err := jsonrepair.Repair(string(raw))
	if err != nil {
		return false
	}
	return json.Unmarshal([]byte(repaired), &probe) == nil
}

// autoAnswerFallbackText is the answer afm synthesizes for an open question
// (no options, only allow_custom) in a non-interactive stage.
const autoAnswerFallbackText = "Прими самое релевантное решение автономно или предложи варианты ответов"

// autoAnswerMarkers are the case-insensitive substrings that mark an option
// as the recommended default, checked in option order (not marker priority
// order) — the first option carrying ANY of these markers wins.
var autoAnswerMarkers = []string{"(recommended)", "(default)", "(рекомендую)", "(рекомендуется)", "(по умолчанию)"}

// PickAutoAnswer chooses the answer afm synthesizes for a question asked by a
// non-interactive stage's agent: the option explicitly marked recommended
// (see autoAnswerMarkers), the first option if none are marked, or a fixed
// fallback text when the question has no options at all.
func PickAutoAnswer(q QuestionFile) (answer string, fromOptions bool) {
	if len(q.Options) == 0 {
		return autoAnswerFallbackText, false
	}
	for _, opt := range q.Options {
		if cleaned, ok := stripRecommendedMarker(opt); ok {
			return cleaned, true
		}
	}
	return q.Options[0], true
}

// stripRecommendedMarker reports whether opt carries any autoAnswerMarkers
// substring and, if so, returns opt with the marker (and a trailing
// space/dash left over from " - (recommended)"-style authoring) removed.
func stripRecommendedMarker(opt string) (string, bool) {
	lower := strings.ToLower(opt)
	for _, m := range autoAnswerMarkers {
		idx := strings.Index(lower, m)
		if idx < 0 {
			continue
		}
		cleaned := opt[:idx] + opt[idx+len(m):]
		cleaned = strings.TrimSpace(cleaned)
		cleaned = strings.TrimRight(cleaned, "-–— \t")
		return strings.TrimSpace(cleaned), true
	}
	return "", false
}

// writeAnswerFile atomically creates <stageDir>/<phase>.<id>.answer.json
// (O_EXCL — a question may only be answered once; returns an error
// satisfying os.IsExist if it already was). No dialog.jsonl side effect —
// callers decide separately whether this answer belongs in human-facing
// history (see WriteAnswer vs WriteInternalAnswer).
func writeAnswerFile(stageDir, phase, id, answer string, fromOptions bool) error {
	answerPath := filepath.Join(stageDir, phase+"."+id+".answer.json")
	payload, err := json.Marshal(map[string]any{
		"id": id, "answer": answer, "from_options": fromOptions,
	})
	if err != nil {
		return fmt.Errorf("marshal answer: %w", err)
	}

	// O_EXCL atomically creates-and-checks-existence in one step, preventing a
	// TOCTOU race where two concurrent writers both see the file missing and
	// both try to create it.
	f, err := os.OpenFile(answerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(payload); err != nil {
		f.Close()
		_ = os.Remove(answerPath)
		return fmt.Errorf("write answer: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(answerPath)
		return fmt.Errorf("sync answer: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(answerPath)
		return fmt.Errorf("close answer: %w", err)
	}
	return nil
}

// WriteAnswer atomically creates <stageDir>/<phase>.<id>.answer.json and
// best-effort appends the answer to <phase>.dialog.jsonl for UI history.
// autoAnswered marks answers afm synthesized itself (non-interactive
// auto-answer), as opposed to a real user reply — see Answer.AutoAnswered /
// Entry.AutoAnswered.
//
// A dialog.jsonl append failure is logged and swallowed: answer.json is
// already safely on disk (the critical path for the agent's polling loop),
// so failing the caller here would incorrectly signal the answer was lost.
func WriteAnswer(stageDir, phase, id, answer string, fromOptions, autoAnswered bool) error {
	if err := writeAnswerFile(stageDir, phase, id, answer, fromOptions); err != nil {
		return err
	}
	dialogPath := filepath.Join(stageDir, phase+".dialog.jsonl")
	if err := AppendAnswer(dialogPath, Answer{
		ID: id, Answer: answer, FromOptions: fromOptions, AutoAnswered: autoAnswered,
	}); err != nil {
		log.Printf("WARN: persist dialog answer for %s/%s.%s: %v (answer.json already written)", stageDir, phase, id, err) //nolint:gosec // G706: phase/id are validated safe filename components by callers
	}
	return nil
}
