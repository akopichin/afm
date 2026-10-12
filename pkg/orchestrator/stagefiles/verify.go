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

// verify/ хранит latest.json, feedback.md и per-pass manifest/report/step-артефакты; latest не разрешает
// завершение стадии.
const (
	verifyDirName     = "verify"
	feedbackFileName  = "feedback.md"
	manifestFileName  = "manifest.json"
	reportFileName    = "report.md"
	rawResultFileName = "raw-result.json"
	resultFileName    = "result.json"
)

// feedbackBudgetBytes — верхняя граница размера verify/feedback.md.
const feedbackBudgetBytes = 12 * 1024

// feedbackTruncationMarker дописывается при обрезании feedback.md по бюджету — явный сигнал
// человеку/агенту, что текст неполный и нужно смотреть report.md по ссылке.
const feedbackTruncationMarker = "\n\n… (обрезано, полный отчёт по ссылке)\n"

// VerifyDir возвращает путь к каталогу verify-namespace стадии.
func VerifyDir(stageDir string) string {
	return filepath.Join(stageDir, verifyDirName)
}

// verificationDir возвращает путь к каталогу одного прохода верификации.
func verificationDir(stageDir, verID string) string {
	return filepath.Join(VerifyDir(stageDir), verID)
}

// newVerificationID — сид генерации id прохода верификации.
var newVerificationID = func() string {
	ts := time.Now().Format("20060102-150405")
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return fmt.Sprintf("v-%s-%s", ts, hex.EncodeToString(b))
}

// NewVerificationID возвращает уникальный id одного прохода верификации — безопасный компонент пути.
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

// atomicWriteFile заменяет файл через temp+rename без fsync; verify-артефакты не имеют гарантий durability
// событийного лога.
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

// SaveRawResult сохраняет сырой ответ модели в step-NN/raw-result.json.
func SaveRawResult(stageDir, verID string, stepIdx int, raw []byte) error {
	path := filepath.Join(StepDir(stageDir, verID, stepIdx), rawResultFileName)
	return atomicWriteFile(path, raw)
}

// SaveAcceptedResult атомарно сохраняет уже провалидированный результат в step-NN/result.json.
func SaveAcceptedResult(stageDir, verID string, stepIdx int, r verify.ModelResult) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal accepted result: %w", err)
	}
	path := filepath.Join(StepDir(stageDir, verID, stepIdx), resultFileName)
	return atomicWriteFile(path, data)
}

// AcceptedResult читает step-NN/result.json.
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

// WriteReport детерминированно рендерит человекочитаемый отчёт прохода в verify/<id>/report.md.
func WriteReport(stageDir, verID string, r verify.ModelResult, alias string, stepIdx int) error {
	path := filepath.Join(verificationDir(stageDir, verID), reportFileName)
	return atomicWriteFile(path, []byte(renderReport(r, alias, stepIdx)))
}

// ReportRelPath возвращает путь к report.md одного прохода верификации ОТНОСИТЕЛЬНО каталога стадии — без
// stageDir.
func ReportRelPath(verID string) string {
	return filepath.Join(verifyDirName, verID, reportFileName)
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

// renderFindingDetailsInto пишет общий блок finding для report и feedback; заголовок и путь задаёт
// вызывающий код.
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

// feedbackProvenancePrefix — начало HTML-комментария в шапке feedback.md, по которому LoadActiveFeedback
// вычленяет verification_id.
const feedbackProvenancePrefix = "<!-- afm-verify verification_id="

// WriteActiveFeedback пишет verify/feedback.md — активные машинные замечания текущего прохода, отдельно от
// human-заметки <stageDir>/feedback.md.
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

// truncateUTF8 обрезает s до limit байт, никогда не разрывая многобайтовую руну посередине. Если s уже
// укладывается в limit — возвращает s без изменений (marker не добавляется).
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

// LoadActiveFeedback читает verify/feedback.md. ok=false если файла нет (нет активной обратной связи от
// верификатора — обычное состояние стадии без запущенной проверки).
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

// ClearActiveFeedback удаляет verify/feedback.md после того как стадия целиком прошла проверку.
func ClearActiveFeedback(stageDir string) error {
	path := filepath.Join(VerifyDir(stageDir), feedbackFileName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove active feedback: %w", err)
	}
	return nil
}
