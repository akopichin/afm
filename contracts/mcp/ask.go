package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/akopichin/afm/contracts/flow"
)

// Machine-readable codes of AskError, printed by `afm stage ask` in its stderr JSON.
const (
	AskCodeInvalidQuestion = "invalid_question"
	AskCodeQuestionPending = "question_pending"
	AskCodeInternal        = "internal"
)

// askAttempts bounds the re-scan after losing a question number to a concurrent writer.
const askAttempts = 5

// AskError is a failed AskQuestion. ID is set for question_pending: the open question the agent must wait for.
type AskError struct {
	Code    string
	Message string
	ID      string
}

func (e *AskError) Error() string { return e.Code + ": " + e.Message }

// AskResult is the question AskQuestion settled on; Created is false when it returned an existing one.
type AskResult struct {
	ID      string
	Created bool
}

// phaseQuestion is one <phase>.<id>.question.json on disk.
type phaseQuestion struct {
	id       string
	text     string
	answered bool
}

// askBeforeLink lets tests take the question number between the scan and the create.
var askBeforeLink func()

// AskQuestion creates the next question file of the phase the way an agent would, but validated and atomic.
// The same question asked again returns the existing id, so a retry after a cut-off command is safe.
func AskQuestion(stageDir, phase string, q QuestionPayload) (AskResult, error) {
	if !flow.IsValidPhase(phase) {
		return AskResult{}, &AskError{Code: AskCodeInvalidQuestion, Message: fmt.Sprintf("phase: %q is not a dialog phase", phase)}
	}
	q.Question = strings.TrimSpace(q.Question)
	// The placeholder id is replaced below; encoding here only rejects a bad question before touching the dir.
	if _, err := encodeQuestion("q1", q); err != nil {
		return AskResult{}, err
	}
	for range askAttempts {
		existing, maxN, err := scanPhaseQuestions(stageDir, phase)
		if err != nil {
			return AskResult{}, &AskError{Code: AskCodeInternal, Message: err.Error()}
		}
		if res, err := existingAsk(existing, q.Question); res != nil || err != nil {
			if err != nil {
				return AskResult{}, err
			}
			return *res, nil
		}
		id := fmt.Sprintf("q%d", maxN+1)
		raw, err := encodeQuestion(id, q)
		if err != nil {
			return AskResult{}, err
		}
		if askBeforeLink != nil {
			askBeforeLink()
		}
		created, err := createFileExclusive(filepath.Join(stageDir, phase+"."+id+".question.json"), raw)
		if err != nil {
			return AskResult{}, &AskError{Code: AskCodeInternal, Message: err.Error()}
		}
		if created {
			return AskResult{ID: id, Created: true}, nil
		}
	}
	return AskResult{}, &AskError{Code: AskCodeInternal, Message: "could not allocate a question id: the stage directory keeps changing"}
}

// existingAsk decides whether the new question text duplicates a question already on disk. A different text
// while another question is open is refused: afm accepts an answer to the oldest open one only.
func existingAsk(existing []phaseQuestion, text string) (*AskResult, error) {
	var open []phaseQuestion
	for _, e := range existing {
		if !e.answered {
			open = append(open, e)
		}
	}
	if len(open) > 0 {
		for _, e := range open {
			if e.text == text {
				return &AskResult{ID: e.id}, nil
			}
		}
		oldest := open[0]
		return nil, &AskError{
			Code: AskCodeQuestionPending, ID: oldest.id,
			Message: fmt.Sprintf("question %s is still waiting for an answer: run `afm stage wait-answer %s` first, then ask the next question", oldest.id, oldest.id),
		}
	}
	// Only the last answered question is deduplicated: the same text asked earlier is a new question.
	if n := len(existing); n > 0 && existing[n-1].text == text {
		return &AskResult{ID: existing[n-1].id}, nil
	}
	return nil, nil
}

// encodeQuestion builds the file bytes and checks them with the same decoder the poller uses, so a file that
// passes here is never rewritten by repair.
func encodeQuestion(id string, q QuestionPayload) ([]byte, error) {
	if q.Question == "" {
		return nil, invalidQuestion("question: must not be empty")
	}
	for i, opt := range q.Options {
		if strings.TrimSpace(opt) == "" {
			return nil, invalidQuestion("options[%d]: must not be empty", i)
		}
		if slices.Contains(q.Options[:i], opt) {
			return nil, invalidQuestion("options[%d]: duplicate of an earlier option %q", i, opt)
		}
	}
	allowCustom := q.AllowCustom == nil || *q.AllowCustom
	if !allowCustom && len(q.Options) == 0 {
		return nil, invalidQuestion("allow_custom: must be true when there are no options, otherwise the user cannot answer")
	}
	q.ID = id
	q.AllowCustom = &allowCustom
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(q); err != nil {
		return nil, &AskError{Code: AskCodeInternal, Message: err.Error()}
	}
	if _, err := DecodeQuestion(buf.Bytes()); err != nil {
		return nil, invalidQuestion("question: %v", err)
	}
	return buf.Bytes(), nil
}

func invalidQuestion(format string, args ...any) *AskError {
	return &AskError{Code: AskCodeInvalidQuestion, Message: fmt.Sprintf(format, args...)}
}

// scanPhaseQuestions lists the phase's question files in q<N> order and the highest question number seen in
// question or answer files, so a number is never reused even when only its answer file is left.
func scanPhaseQuestions(stageDir, phase string) ([]phaseQuestion, int, error) {
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		return nil, 0, err
	}
	var qs []phaseQuestion
	maxN := 0
	for _, ent := range entries {
		name, ok := strings.CutPrefix(ent.Name(), phase+".")
		if !ok || ent.IsDir() {
			continue
		}
		if id, ok := strings.CutSuffix(name, ".answer.json"); ok {
			maxN = max(maxN, questionNumber(id))
			continue
		}
		id, ok := strings.CutSuffix(name, ".question.json")
		if !ok || !ValidQuestionID(id) {
			continue
		}
		maxN = max(maxN, questionNumber(id))
		qs = append(qs, phaseQuestion{id: id, text: readQuestionText(filepath.Join(stageDir, ent.Name())), answered: answerFileExists(stageDir, phase, id)})
	}
	slices.SortFunc(qs, func(a, b phaseQuestion) int {
		switch {
		case AskOrderLess(a.id, b.id):
			return -1
		case AskOrderLess(b.id, a.id):
			return 1
		default:
			return 0
		}
	})
	return qs, maxN, nil
}

func questionNumber(id string) int {
	n, _ := askOrder(id)
	return n
}

// readQuestionText is empty for an unreadable or invalid file: it then never matches a new question.
func readQuestionText(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	q, _, _, err := RecoverQuestion(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(q.Question)
}

func answerFileExists(stageDir, phase, id string) bool {
	_, err := os.Stat(answerPath(stageDir, phase, id))
	return err == nil
}

func answerPath(stageDir, phase, id string) string {
	return filepath.Join(stageDir, phase+"."+id+".answer.json")
}

// createFileExclusive publishes raw at path only if path does not exist, and never exposes a partial file:
// the bytes are synced in a temp file and hard-linked into place. created=false means the name was taken.
func createFileExclusive(path string, raw []byte) (bool, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ask-*.tmp")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(raw)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err != nil {
		return false, err
	}
	switch err := os.Link(tmp.Name(), path); {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrExist):
		return false, nil
	default:
		// A filesystem without hard links: O_EXCL still never overwrites, at the cost of a visible partial write.
		return writeFileExclusive(path, raw)
	}
}

func writeFileExclusive(path string, raw []byte) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return false, err
	}
	return true, nil
}

// ValidQuestionID accepts the characters a question id may have in a file name: no separators, no dots.
func ValidQuestionID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// LastQuestionID is the newest question of the phase, answered or not.
func LastQuestionID(stageDir, phase string) (string, bool) {
	qs, _, err := scanPhaseQuestions(stageDir, phase)
	if err != nil || len(qs) == 0 {
		return "", false
	}
	return qs[len(qs)-1].id, true
}

// ReadAnswer reads the answer file. A missing, empty or half-written file is "no answer yet": the writer
// may still be between create and write.
func ReadAnswer(stageDir, phase, id string) (Answer, bool) {
	raw, err := os.ReadFile(answerPath(stageDir, phase, id))
	if err != nil {
		return Answer{}, false
	}
	var probe struct {
		Answer      *string `json:"answer"`
		FromOptions bool    `json:"from_options"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.Answer == nil {
		return Answer{}, false
	}
	return Answer{ID: id, Answer: *probe.Answer, FromOptions: probe.FromOptions}, true
}

// WaitForAnswer polls until the answer appears. ok=false with a nil error is a plain timeout: the caller
// reports "still waiting" and the agent repeats the command.
func WaitForAnswer(ctx context.Context, stageDir, phase, id string, timeout, interval time.Duration) (Answer, bool, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if a, ok := ReadAnswer(stageDir, phase, id); ok {
			return a, true, nil
		}
		select {
		case <-ctx.Done():
			return Answer{}, false, ctx.Err()
		case <-deadline.C:
			return Answer{}, false, nil
		case <-ticker.C:
		}
	}
}
