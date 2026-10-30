// Package mcp implements the MCP (Model Context Protocol) HTTP server
// for the afm ask_user tool, plus per-phase dialog persistence.
package mcp

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaptinlin/jsonrepair"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/jsonstrict"
	"github.com/akopichin/afm/pkg/memory"
	"github.com/akopichin/afm/schema"
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
				if e.Answer != nil && e.Question != "" {
					// A COMPLETE prior exchange exists for this id and a fresh question line arrived — the agent reused
					// the id for a NEW question.
					e.TS = q.TS
					e.Question = q.Question
					e.Options = q.Options
					e.AllowCustom = q.AllowCustom
					e.Answer = nil
					e.AnswerTS = ""
					e.FromOptions = false
					e.AutoAnswered = false
				} else {
					// Same incarnation: either the answer line arrived before the question line or a still-unanswered
					// question was rewritten. Fill in / refresh the question fields, leaving any answer untouched.
					e.TS = q.TS
					e.Question = q.Question
					e.Options = q.Options
					e.AllowCustom = q.AllowCustom
				}
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

// appendMu serializes concurrent writers of dialog.jsonl.
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
	// Malformed is true when the file's JSON could not be parsed even after jsonrepair.
	Malformed bool
}

// FindUnansweredQuestions scans stageDir for *.question.json files that do not
// have a matching *.answer.json. Filenames must follow "<phase>.<id>.question.json".
func FindUnansweredQuestions(stageDir string) ([]QuestionFile, error) {
	return scanUnansweredQuestions(stageDir, nil)
}

func scanUnansweredQuestions(stageDir string, beforeRepair func()) ([]QuestionFile, error) {
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []QuestionFile
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".question.json") {
			continue
		}
		qPath := filepath.Join(stageDir, ent.Name())
		base := strings.TrimSuffix(ent.Name(), ".question.json")
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
		qf, fixed, source, decodeErr := RecoverQuestion(raw)
		malformed := decodeErr != nil
		if malformed {
			log.Printf("WARN: %s: invalid question JSON: %v", qPath, decodeErr)
			qf = QuestionPayload{Question: string(raw)}
		} else if !bytes.Equal(raw, fixed) {
			if beforeRepair != nil {
				beforeRepair()
			}
			committed, err := persistQuestionRepair(qPath, raw, fixed, source)
			if err != nil {
				log.Printf("WARN: %s: failed to persist question repair: %v", qPath, err)
			}
			if !committed {
				continue
			}
		}

		// The FILENAME id is authoritative — it's what the agent's polling loop derives its answer path from and
		// what the answerPath existence check above already keys off.
		if id == "" {
			continue
		}
		allowCustom := true
		if qf.AllowCustom != nil {
			allowCustom = *qf.AllowCustom
		}
		out = append(out, QuestionFile{
			Phase: phase, ID: id, Question: qf.Question,
			Options: qf.Options, AllowCustom: allowCustom, Malformed: malformed,
		})
	}
	return out, nil
}

// QuestionPayload keeps allow_custom optional: omitted and null both default to true.
type QuestionPayload struct {
	ID          string   `json:"id"`
	Question    string   `json:"question"`
	Options     []string `json:"options"`
	AllowCustom *bool    `json:"allow_custom"`
}

const MaxQuestionBytes = 256 * 1024
const maxQuestionRepairBytes = 16 * 1024

// DecodeQuestion is the strict validator shared by file scans and format recovery.
func DecodeQuestion(raw []byte) (QuestionPayload, error) {
	if err := jsonstrict.EnforceMaxBytes(raw, MaxQuestionBytes); err != nil {
		return QuestionPayload{}, err
	}
	if err := jsonstrict.RejectFence(bytes.TrimSpace(raw)); err != nil {
		return QuestionPayload{}, err
	}
	if err := jsonstrict.DecodeNoDuplicates(raw); err != nil {
		return QuestionPayload{}, err
	}
	var q QuestionPayload
	if err := jsonstrict.DecodeDisallowUnknown(raw, &q); err != nil {
		return QuestionPayload{}, err
	}
	if err := schema.Validate(schema.Question, raw); err != nil {
		return QuestionPayload{}, jsonstrict.NewIssue(jsonstrict.StructureIssue, err)
	}
	return q, nil
}

// RecoverQuestion bounds syntax repair and returns accepted bytes without changing the original input.
func RecoverQuestion(raw []byte) (QuestionPayload, []byte, jsonstrict.RepairKind, error) {
	q, original := DecodeQuestion(raw)
	if original == nil {
		return q, raw, "", nil
	}
	if len(raw) > MaxQuestionBytes || errors.Is(original, jsonstrict.ErrDuplicateKey) {
		return QuestionPayload{}, nil, "", original
	}
	if len(bytes.TrimSpace(raw)) > maxQuestionRepairBytes {
		return QuestionPayload{}, nil, "", original
	}
	input := quoteQuestionKeys(bytes.TrimSpace(raw))
	_, complete := jsonstrict.SingleObject(input)
	if !complete {
		return QuestionPayload{}, nil, "", jsonstrict.NewIssue(jsonstrict.RefusalIssue, original)
	}
	// Valid documents with an invalid shape must not pass through a syntax repairer.
	if json.Valid(raw) {
		return QuestionPayload{}, nil, "", original
	}
	if err := jsonstrict.DecodeNoDuplicates(input); errors.Is(err, jsonstrict.ErrDuplicateKey) {
		return QuestionPayload{}, nil, "", err
	}
	fixed, err := jsonrepair.Repair(string(input))
	if err == nil {
		if q, err := DecodeQuestion([]byte(fixed)); err == nil {
			return q, []byte(fixed), jsonstrict.MechanicalRepair, nil
		}
	}
	return QuestionPayload{}, nil, "", original
}

// A missing opening key quote otherwise looks like an unfinished string to the boundary scanner.
func quoteQuestionKeys(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	inString, escaped, keyPosition := false, false, false
	for i, b := range raw {
		if !inString && keyPosition && b != ' ' && b != '\n' && b != '\r' && b != '\t' {
			for _, key := range []string{"id", "question", "options", "allow_custom"} {
				prefix := key + `"`
				if bytes.HasPrefix(raw[i:], []byte(prefix)) && bytes.HasPrefix(bytes.TrimSpace(raw[i+len(prefix):]), []byte(":")) {
					out = append(out, '"')
					inString = true
					break
				}
			}
			keyPosition = false
		}
		out = append(out, b)
		if inString {
			switch {
			case escaped:
				escaped = false
			case b == '\\':
				escaped = true
			case b == '"':
				inString = false
			default:
				// Any other byte inside a string is content.
			}
		} else {
			switch b {
			case '"':
				inString = true
			case '{', ',':
				keyPosition = true
			default:
				// Other bytes keep the current key position.
			}
		}
	}
	return out
}

var questionRepairMu sync.Mutex

func persistQuestionRepair(path string, raw, fixed []byte, source jsonstrict.RepairKind) (bool, error) {
	committed, err := CommitQuestionRepair(path, raw, fixed)
	if err != nil || !committed {
		return false, err
	}
	rawHash := fmt.Sprintf("%x", sha256.Sum256(raw))
	receipt, err := json.Marshal(map[string]any{
		"contract": schema.Question, "owner": "question/" + filepath.Base(path),
		"source": source, "validation": "accepted", "raw_sha256": rawHash,
		"candidate_sha256": fmt.Sprintf("%x", sha256.Sum256(fixed)), "attempts": 0,
	})
	if err != nil {
		return false, err
	}
	return true, memory.AtomicWrite(path+"."+rawHash+".recovery.json", receipt)
}

// CommitQuestionRepair fences both local and model candidates against a newer question or an answer.
func CommitQuestionRepair(path string, raw, fixed []byte) (bool, error) {
	questionRepairMu.Lock()
	defer questionRepairMu.Unlock()
	if _, err := DecodeQuestion(fixed); err != nil {
		return false, err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(current, raw) {
		return false, nil
	}
	answerPath := strings.TrimSuffix(path, ".question.json") + ".answer.json"
	if _, err := os.Stat(answerPath); !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := preserveQuestionRaw(path, raw); err != nil {
		return false, err
	}
	if err := memory.AtomicWrite(path, fixed); err != nil {
		return false, err
	}
	return true, nil
}

func preserveQuestionRaw(path string, raw []byte) error {
	// Reused question IDs need separate immutable snapshots for distinct payloads.
	for _, snapshot := range []string{path + ".raw", fmt.Sprintf("%s.%x.raw", path, sha256.Sum256(raw))} {
		f, err := os.OpenFile(snapshot, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			saved, err := os.ReadFile(snapshot)
			if err != nil {
				return err
			}
			if bytes.Equal(saved, raw) {
				return nil
			}
			continue
		}
		if err != nil {
			return err
		}
		_, writeErr := f.Write(raw)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	return errors.New("question raw snapshot differs from its content hash")
}

func CanParseQuestion(raw []byte) bool {
	_, _, _, err := RecoverQuestion(raw)
	return err == nil
}

// autoAnswerFallbackText is the answer afm synthesizes for an open question
// (no options, only allow_custom) in a non-interactive stage.
const autoAnswerFallbackText = "Прими самое релевантное решение автономно или предложи варианты ответов"

// autoAnswerMarkers are the case-insensitive substrings that mark an option as the recommended default,
// checked in option order — the first option carrying ANY of these markers wins.
var autoAnswerMarkers = []string{"(recommended)", "(default)", "(рекомендую)", "(рекомендуется)", "(по умолчанию)"}

// PickAutoAnswer chooses a recommended option, otherwise the first option or fixed fallback text when
// options are absent.
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

// stripRecommendedMarker reports whether opt carries any autoAnswerMarkers substring and, if so, returns
// opt with the marker removed.
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

// WriteAnswerFile atomically creates <stageDir>/<phase>.<id>.answer.json.
func WriteAnswerFile(stageDir, phase, id, answer string, fromOptions bool) error {
	questionRepairMu.Lock()
	defer questionRepairMu.Unlock()
	answerPath := filepath.Join(stageDir, phase+"."+id+".answer.json")
	payload, err := json.Marshal(map[string]any{
		"id": id, "answer": answer, "from_options": fromOptions,
	})
	if err != nil {
		return fmt.Errorf("marshal answer: %w", err)
	}

	// O_EXCL atomically creates-and-checks-existence in one step, preventing a TOCTOU race where two
	// concurrent writers both see the file missing and both try to create it.
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

// DialogPath returns the phase's dialog-history file inside a stage directory.
func DialogPath(stageDir, phase string) string {
	return filepath.Join(stageDir, phase+".dialog.jsonl")
}

// WriteAnswer atomically creates <stageDir>/<phase>.<id>.answer.json and best-effort appends the answer to
// <phase>.dialog.jsonl for UI history.
func WriteAnswer(stageDir, phase, id, answer string, fromOptions, autoAnswered bool) error {
	if err := WriteAnswerFile(stageDir, phase, id, answer, fromOptions); err != nil {
		return err
	}
	dialogPath := DialogPath(stageDir, phase)
	if err := AppendAnswer(dialogPath, Answer{
		ID: id, Answer: answer, FromOptions: fromOptions, AutoAnswered: autoAnswered,
	}); err != nil {
		log.Printf("WARN: persist dialog answer for %s/%s.%s: %v (answer.json already written)", stageDir, phase, id, err) //nolint:gosec // G706: phase/id are validated safe filename components by callers
	}
	return nil
}

// askOrder parses the strict q<N> form: the id must be 'q' followed by one or more digits and nothing
// else. foo2/bar10/qX are NOT numeric — they fall back to lexical order in AskOrderLess.
func askOrder(id string) (int, bool) {
	if len(id) < 2 || id[0] != 'q' {
		return 0, false
	}
	for i := 1; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil {
		return 0, false // overflow → treat as non-numeric
	}
	return n, true
}

// AskOrderLess orders question ids by the q<N> convention the interactive prompt mandates: numeric suffix
// order, a numeric id before a non-numeric one, else lexical.
func AskOrderLess(a, b string) bool {
	na, oka := askOrder(a)
	nb, okb := askOrder(b)
	switch {
	case oka && okb:
		if na != nb {
			return na < nb
		}
		return a < b
	case oka:
		return true
	case okb:
		return false
	default:
		return a < b
	}
}

// phaseRank orders phases by their lifecycle position using flow.Phases. A higher rank is a later,
// more-recently-active phase.
func phaseRank(phase string) int {
	for i, p := range flow.Phases() {
		if string(p) == phase {
			return i
		}
	}
	return -1
}

func questionBefore(a, b QuestionFile) bool {
	if a.Phase != b.Phase {
		// Prefer the later phase's question: earlier open questions are stale and can hide the active agent's
		// question.
		return phaseRank(a.Phase) > phaseRank(b.Phase)
	}
	return AskOrderLess(a.ID, b.ID)
}

// SelectCurrentQuestion returns the oldest unanswered question in qs — the one the agent's sequential
// polling loop is blocked on and the ONLY one afm should surface / accept an answer for.
func SelectCurrentQuestion(qs []QuestionFile) (QuestionFile, bool) {
	var cur QuestionFile
	found := false
	for _, q := range qs {
		if !found || questionBefore(q, cur) {
			cur = q
			found = true
		}
	}
	return cur, found
}

// CurrentQuestion is the FS-backed convenience wrapper for server read/answer paths. It propagates the
// FindUnansweredQuestions error so callers can fail closed.
func CurrentQuestion(stageDir string) (QuestionFile, bool, error) {
	qs, err := FindUnansweredQuestions(stageDir)
	if err != nil {
		return QuestionFile{}, false, err
	}
	cur, ok := SelectCurrentQuestion(qs)
	return cur, ok, nil
}
