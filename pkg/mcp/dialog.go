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
	ID          string `json:"id"`
	TS          string `json:"ts"`
	Answer      string `json:"answer"`
	FromOptions bool   `json:"from_options"`
}

// Entry is a grouped Q/A pair for reading. Answer is nil when the question
// is still open.
type Entry struct {
	ID          string
	TS          string
	Question    string
	Options     []string
	AllowCustom bool
	Answer      *string
	AnswerTS    string
	FromOptions bool
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
		if err := json.Unmarshal(raw, &qf); err != nil {
			// Агент пишет question.json руками (файловый протокол диалога) и
			// иногда ломает JSON: незаэкранированная кавычка внутри строки,
			// пропущенная кавычка у ключа, пропущенная запятая и т.п.
			// jsonrepair покрывает эти классы ошибок гораздо шире одной
			// самописной эвристики. Если даже она не справилась — файл
			// вопроса раньше молча пропадал из вида поллера (continue), и
			// стадия зависала в awaiting-forever без единого следа. Вместо
			// этого показываем вопрос-заглушку с сырым содержимым файла:
			// стадия всегда доходит до awaiting_user_input.
			repaired, repairErr := jsonrepair.Repair(string(raw))
			if repairErr == nil {
				repairErr = json.Unmarshal([]byte(repaired), &qf)
			}
			if repairErr != nil {
				log.Printf("WARN: %s: invalid JSON, surfacing as fallback stub: %v", qPath, err)
				preview := raw
				if len(preview) > 400 {
					preview = preview[:400]
				}
				qf.Question = fmt.Sprintf("⚠️ The agent wrote a malformed question.json that afm could not parse or repair. The file was left on disk for inspection.\n\nRaw preview:\n%s", preview)
				qf.Options = []string{"Continue anyway", "Cancel stage"}
				allowCustom := true
				qf.AllowCustom = &allowCustom
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
			Options: qf.Options, AllowCustom: allowCustom,
		})
	}
	return out, nil
}
