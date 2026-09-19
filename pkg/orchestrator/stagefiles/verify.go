package stagefiles

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

// Пространство файлов AI-verify под стадией:
//
//	<stageDir>/verify/
//	  latest.json                 # указатель "последняя проверка" для отображения —
//	                               # НЕ разрешение на завершение стадии (пишет V4)
//	  feedback.md                 # активные машинные замечания (провенанс), НЕ human-заметка
//	  <verification-id>/
//	    manifest.json             # метаданные + результаты шагов этого прохода
//	    report.md                 # человекочитаемый отчёт по проходу
//	    step-01/ command.log result.json
//	    step-02/ agent.log agent.jsonl agent.stderr.log raw-result.json result.json
//
// Это только хранилище: пакет ничего не знает про то, что значит "стадия
// завершена" — это решает V4/V5 (см. AcceptedResult и LoadActiveFeedback).
const (
	verifyDirName     = "verify"
	feedbackFileName  = "feedback.md"
	manifestFileName  = "manifest.json"
	reportFileName    = "report.md"
	rawResultFileName = "raw-result.json"
	resultFileName    = "result.json"
)

// feedbackBudgetBytes — верхняя граница размера verify/feedback.md (не
// report.md — тот всегда пишется целиком). Feedback инлайнится в контекст
// следующего запуска агента, поэтому его размер ограничен; полный отчёт
// всегда доступен по ссылке на диске.
const feedbackBudgetBytes = 12 * 1024

// feedbackTruncationMarker дописывается при обрезании feedback.md по
// бюджету — явный сигнал человеку/агенту, что текст неполный и нужно
// смотреть report.md по ссылке.
const feedbackTruncationMarker = "\n\n… (обрезано, полный отчёт по ссылке)\n"

// VerifyDir возвращает путь к каталогу verify-namespace стадии.
func VerifyDir(stageDir string) string {
	return filepath.Join(stageDir, verifyDirName)
}

// verificationDir возвращает путь к каталогу одного прохода верификации.
func verificationDir(stageDir, verID string) string {
	return filepath.Join(VerifyDir(stageDir), verID)
}

// newVerificationID — сид генерации id прохода верификации. Пакетная
// переменная (не константа), чтобы тесты могли подменить её на
// детерминированную функцию и получать воспроизводимые id — то же самое
// решение, что state.SavePreNote/newRunID применяют к времени/рандому.
var newVerificationID = func() string {
	ts := time.Now().Format("20060102-150405")
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return fmt.Sprintf("v-%s-%s", ts, hex.EncodeToString(b))
}

// NewVerificationID возвращает уникальный id одного прохода верификации —
// безопасный компонент пути (без "/", без пробелов). Это НЕ номер локальной
// retry-попытки шага внутри прохода (за него отвечает stepIdx) — id меняется
// только когда стадия запускает верификацию заново с нуля.
func NewVerificationID() string {
	return newVerificationID()
}

// StepDir возвращает путь к каталогу одного шага прохода верификации.
// stepIdx нумеруется с 1, в имени каталога — с ведущим нулём (step-01).
func StepDir(stageDir, verID string, stepIdx int) string {
	return filepath.Join(verificationDir(stageDir, verID), fmt.Sprintf("step-%02d", stepIdx))
}

// ManifestStep — результат одного шага прохода верификации внутри manifest.json.
type ManifestStep struct {
	Index   int    `json:"index"`
	Kind    string `json:"kind"`              // "shell" | "agent"
	Command string `json:"command,omitempty"` // shell-команда или алиас агента-исполнителя
	Outcome string `json:"outcome"`           // verdict модели либо служебный исход ("error", "timeout", …)
}

// Manifest — метаданные одного прохода верификации: какая стадия/run/фаза
// его запустили и что вернул каждый шаг.
type Manifest struct {
	RunID          string         `json:"run_id"`
	StageID        string         `json:"stage_id"`
	Phase          string         `json:"phase"`
	VerificationID string         `json:"verification_id"`
	CreatedAt      time.Time      `json:"created_at"`
	Steps          []ManifestStep `json:"steps"`
}

// atomicWriteFile пишет data во временный файл рядом с path и переименовывает
// его на место — то же самое temp+rename, что state.SavePreNote (см.
// pkg/state/state.go), без лишнего fsync: verify-файлы не входят в
// событийный лог и не требуют его гарантий durability.
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write temp %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// SaveRawResult сохраняет сырой (ещё не разобранный/не принятый) ответ
// модели в step-NN/raw-result.json. Пишется до валидации DecodeModelResult —
// это единственный след того, что шаг вообще что-то вернул, если разбор или
// проверка согласованности упадёт.
func SaveRawResult(stageDir, verID string, stepIdx int, raw []byte) error {
	path := filepath.Join(StepDir(stageDir, verID, stepIdx), rawResultFileName)
	return atomicWriteFile(path, raw)
}

// SaveAcceptedResult атомарно сохраняет уже провалидированный результат в
// step-NN/result.json. Наличие этого файла — единственный признак того, что
// шаг "принят" (см. AcceptedResult) — raw-result.json без result.json значит
// незавершённый/отклонённый разбор.
func SaveAcceptedResult(stageDir, verID string, stepIdx int, r verify.ModelResult) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal accepted result: %w", err)
	}
	path := filepath.Join(StepDir(stageDir, verID, stepIdx), resultFileName)
	return atomicWriteFile(path, data)
}

// AcceptedResult читает step-NN/result.json. ok=false означает "шаг не
// принят" — либо result.json ещё не появился (шаг в процессе/упал до
// сохранения), либо есть только raw-result.json без result.json
// (частичный коммит: разбор начался, но не завершился успехом).
func AcceptedResult(stageDir, verID string, stepIdx int) (verify.ModelResult, bool, error) {
	path := filepath.Join(StepDir(stageDir, verID, stepIdx), resultFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return verify.ModelResult{}, false, nil
		}
		return verify.ModelResult{}, false, fmt.Errorf("read accepted result: %w", err)
	}
	var r verify.ModelResult
	if err := json.Unmarshal(data, &r); err != nil {
		return verify.ModelResult{}, false, fmt.Errorf("parse accepted result: %w", err)
	}
	return r, true, nil
}

// WriteReport детерминированно рендерит человекочитаемый отчёт прохода в
// verify/<id>/report.md. В отличие от feedback.md, отчёт никогда не
// обрезается — это полный источник истины для человека, открывающего
// проход вручную.
func WriteReport(stageDir, verID string, r verify.ModelResult, alias string, stepIdx int) error {
	path := filepath.Join(verificationDir(stageDir, verID), reportFileName)
	return atomicWriteFile(path, []byte(renderReport(r, alias, stepIdx)))
}

func renderReport(r verify.ModelResult, alias string, stepIdx int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Отчёт проверки: %s, шаг %d\n\n", alias, stepIdx)
	fmt.Fprintf(&b, "Вердикт: %s\n\n", r.Verdict)
	fmt.Fprintf(&b, "## Summary\n\n%s\n\n", r.Summary)
	fmt.Fprintf(&b, "## Findings (%d)\n\n", len(r.Findings))
	if len(r.Findings) == 0 {
		b.WriteString("Замечаний нет.\n")
	}
	for _, f := range r.Findings {
		renderFindingInto(&b, f)
	}
	return b.String()
}

func renderFindingInto(b *strings.Builder, f verify.Finding) {
	status := "не блокирующее"
	if f.Blocking {
		status = "блокирующее"
	}
	fmt.Fprintf(b, "### %s (%s)\n\n", f.Title, status)
	if f.Path != nil {
		loc := *f.Path
		if f.LineStart != nil {
			loc += fmt.Sprintf(":%d", *f.LineStart)
			if f.LineEnd != nil && *f.LineEnd != *f.LineStart {
				loc += fmt.Sprintf("-%d", *f.LineEnd)
			}
		}
		fmt.Fprintf(b, "- Путь: %s\n", loc)
	}
	renderFindingDetailsInto(b, f)
}

// renderFindingDetailsInto пишет общий для report.md и feedback.md блок
// "Требование/Свидетельство/Минимальное исправление" одного finding —
// вызывающая сторона сама решает, что писать до него (заголовок, статус,
// путь), т.к. эта часть в report.md и feedback.md разная.
func renderFindingDetailsInto(b *strings.Builder, f verify.Finding) {
	fmt.Fprintf(b, "- Требование: %s\n", f.Requirement)
	fmt.Fprintf(b, "- Свидетельство: %s\n", f.Evidence)
	fmt.Fprintf(b, "- Минимальное исправление: %s\n\n", f.MinimalFix)
}

// WriteManifest атомарно сохраняет метаданные прохода в verify/<id>/manifest.json.
func WriteManifest(stageDir, verID string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	path := filepath.Join(verificationDir(stageDir, verID), manifestFileName)
	return atomicWriteFile(path, data)
}

// feedbackProvenancePrefix — начало HTML-комментария в шапке feedback.md, по
// которому LoadActiveFeedback вычленяет verification_id. Формат:
// "<!-- afm-verify verification_id=<verID> step=<stepIdx> -->".
const feedbackProvenancePrefix = "<!-- afm-verify verification_id="

// WriteActiveFeedback пишет verify/feedback.md — активные машинные замечания
// текущего прохода, отдельно от human-заметки <stageDir>/feedback.md.
// Содержит вердикт, ссылку на полный report.md, провенанс (verID+stepIdx —
// по нему LoadActiveFeedback/вызывающий код позже проверяют свежесть) и
// текст каждого блокирующего finding. Обрезается по feedbackBudgetBytes
// (report.md на диске при этом остаётся полным).
func WriteActiveFeedback(stageDir, verID string, r verify.ModelResult, alias string, stepIdx int) error {
	content := renderActiveFeedback(stageDir, verID, r, alias, stepIdx)
	content = truncateUTF8(content, feedbackBudgetBytes, feedbackTruncationMarker)
	path := filepath.Join(VerifyDir(stageDir), feedbackFileName)
	return atomicWriteFile(path, []byte(content))
}

func renderActiveFeedback(stageDir, verID string, r verify.ModelResult, alias string, stepIdx int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s step=%d -->\n\n", feedbackProvenancePrefix, verID, stepIdx)
	fmt.Fprintf(&b, "# Проверка: %s\n\n", r.Verdict)
	fmt.Fprintf(&b, "Проверка: %s, шаг %d\n\n", alias, stepIdx)
	fmt.Fprintf(&b, "Полный отчёт: %s\n\n", filepath.Join(verificationDir(stageDir, verID), reportFileName))

	blocking := blockingFindings(r.Findings)
	if len(blocking) == 0 {
		b.WriteString("Блокирующих замечаний нет.\n")
		return b.String()
	}
	b.WriteString("## Блокирующие замечания\n\n")
	for _, f := range blocking {
		fmt.Fprintf(&b, "### %s\n\n", f.Title)
		renderFindingDetailsInto(&b, f)
	}
	return b.String()
}

func blockingFindings(fs []verify.Finding) []verify.Finding {
	out := make([]verify.Finding, 0, len(fs))
	for _, f := range fs {
		if f.Blocking {
			out = append(out, f)
		}
	}
	return out
}

// truncateUTF8 обрезает s до limit байт (включая marker), никогда не разрывая
// многобайтовую руну посередине. Если s уже укладывается в limit — возвращает
// s без изменений (marker не добавляется).
func truncateUTF8(s string, limit int, marker string) string {
	if len(s) <= limit {
		return s
	}
	cut := limit - len(marker)
	if cut < 0 {
		cut = 0
	}
	if cut > len(s) {
		cut = len(s)
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + marker
}

// LoadActiveFeedback читает verify/feedback.md. ok=false если файла нет
// (нет активной обратной связи от верификатора — обычное состояние стадии
// без запущенной проверки). verID вычленяется из провенанс-комментария в
// шапке файла — по нему вызывающий код может сверить свежесть (совпадает ли
// с id последнего завершённого прохода).
func LoadActiveFeedback(stageDir string) (text string, verID string, ok bool) {
	data, err := os.ReadFile(filepath.Join(VerifyDir(stageDir), feedbackFileName))
	if err != nil {
		return "", "", false
	}
	return string(data), parseFeedbackVerificationID(string(data)), true
}

func parseFeedbackVerificationID(text string) string {
	idx := strings.Index(text, feedbackProvenancePrefix)
	if idx < 0 {
		return ""
	}
	rest := text[idx+len(feedbackProvenancePrefix):]
	end := strings.IndexByte(rest, ' ')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// ClearActiveFeedback удаляет verify/feedback.md после того как стадия
// целиком прошла проверку (или её результат больше не актуален). Архив
// прохода в verify/<id>/ (manifest.json, report.md, шаги) не трогается —
// это история, а feedback.md — только текущее активное состояние.
func ClearActiveFeedback(stageDir string) error {
	path := filepath.Join(VerifyDir(stageDir), feedbackFileName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove active feedback: %w", err)
	}
	return nil
}
