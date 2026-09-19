package stagefiles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

func strPtr(v string) *string { return &v }

func sampleAcceptedResult() verify.ModelResult {
	return verify.ModelResult{
		SchemaVersion: 1,
		Verdict:       verify.VerdictNeedsChanges,
		Summary:       "найдена одна блокирующая проблема",
		Findings: []verify.Finding{{
			Blocking:    true,
			Title:       "нет теста на edge-case",
			Path:        strPtr("pkg/foo/foo.go"),
			LineStart:   intPtr(12),
			LineEnd:     intPtr(20),
			Requirement: "покрыть тестом граничный случай",
			Evidence:    "grep по _test.go ничего не нашёл",
			MinimalFix:  "добавить TestFoo_EdgeCase",
		}},
	}
}

func intPtr(v int) *int { return &v }

func TestSaveRawResult_ThenSaveAcceptedResult_RoundTrips(t *testing.T) {
	stageDir := t.TempDir()
	verID := "ver-1"
	raw := []byte(`{"schema_version":1,"verdict":"needs_changes"}`)

	if err := SaveRawResult(stageDir, verID, 1, raw); err != nil {
		t.Fatalf("SaveRawResult: %v", err)
	}
	rawPath := filepath.Join(StepDir(stageDir, verID, 1), rawResultFileName)
	got, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read raw-result.json: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("raw-result.json content mismatch: got %q, want %q", got, raw)
	}

	want := sampleAcceptedResult()
	if err := SaveAcceptedResult(stageDir, verID, 1, want); err != nil {
		t.Fatalf("SaveAcceptedResult: %v", err)
	}
	resultPath := filepath.Join(StepDir(stageDir, verID, 1), resultFileName)
	if _, err := os.Stat(resultPath); err != nil {
		t.Fatalf("result.json not created: %v", err)
	}

	back, ok, err := AcceptedResult(stageDir, verID, 1)
	if err != nil {
		t.Fatalf("AcceptedResult: %v", err)
	}
	if !ok {
		t.Fatal("AcceptedResult: expected ok=true")
	}
	if back.Verdict != want.Verdict || back.Summary != want.Summary || len(back.Findings) != len(want.Findings) {
		t.Fatalf("AcceptedResult round-trip mismatch: got %+v, want %+v", back, want)
	}
	if back.Findings[0].Title != want.Findings[0].Title || *back.Findings[0].Path != *want.Findings[0].Path {
		t.Fatalf("AcceptedResult finding round-trip mismatch: got %+v, want %+v", back.Findings[0], want.Findings[0])
	}
}

func TestAcceptedResult_PartialCommit_RawWithoutResult(t *testing.T) {
	stageDir := t.TempDir()
	verID := "ver-1"

	if err := SaveRawResult(stageDir, verID, 1, []byte(`{"broken`)); err != nil {
		t.Fatalf("SaveRawResult: %v", err)
	}

	_, ok, err := AcceptedResult(stageDir, verID, 1)
	if err != nil {
		t.Fatalf("AcceptedResult should not error on absent result.json: %v", err)
	}
	if ok {
		t.Fatal("AcceptedResult: expected ok=false for a partial commit (raw without accepted result)")
	}
}

func TestWriteActiveFeedback_And_LoadActiveFeedback_RoundTrip(t *testing.T) {
	stageDir := t.TempDir()
	verID := "ver-42"
	r := sampleAcceptedResult()

	if err := WriteReport(stageDir, verID, r, "codex", 1); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	if err := WriteActiveFeedback(stageDir, verID, r, "codex", 1); err != nil {
		t.Fatalf("WriteActiveFeedback: %v", err)
	}

	text, gotVerID, ok := LoadActiveFeedback(stageDir)
	if !ok {
		t.Fatal("LoadActiveFeedback: expected ok=true")
	}
	if gotVerID != verID {
		t.Fatalf("LoadActiveFeedback verID: got %q, want %q", gotVerID, verID)
	}
	reportLink := filepath.Join(VerifyDir(stageDir), verID, reportFileName)
	if !strings.Contains(text, reportLink) {
		t.Fatalf("feedback.md missing report link %q; got:\n%s", reportLink, text)
	}
	if !strings.Contains(text, r.Findings[0].Title) {
		t.Fatalf("feedback.md missing blocking finding title; got:\n%s", text)
	}
	if !strings.Contains(text, r.Findings[0].Requirement) || !strings.Contains(text, r.Findings[0].Evidence) ||
		!strings.Contains(text, r.Findings[0].MinimalFix) {
		t.Fatalf("feedback.md missing finding detail fields; got:\n%s", text)
	}
}

func TestNewPass_DoesNotOverwritePreviousPassDir(t *testing.T) {
	stageDir := t.TempDir()
	first := sampleAcceptedResult()
	second := sampleAcceptedResult()
	second.Summary = "второй проход, другая проблема"

	if err := SaveAcceptedResult(stageDir, "ver-1", 1, first); err != nil {
		t.Fatalf("SaveAcceptedResult(ver-1): %v", err)
	}
	if err := SaveAcceptedResult(stageDir, "ver-2", 1, second); err != nil {
		t.Fatalf("SaveAcceptedResult(ver-2): %v", err)
	}

	got1, ok, err := AcceptedResult(stageDir, "ver-1", 1)
	if err != nil || !ok {
		t.Fatalf("AcceptedResult(ver-1): ok=%v err=%v", ok, err)
	}
	got2, ok, err := AcceptedResult(stageDir, "ver-2", 1)
	if err != nil || !ok {
		t.Fatalf("AcceptedResult(ver-2): ok=%v err=%v", ok, err)
	}
	if got1.Summary == got2.Summary {
		t.Fatal("both verification passes report the same summary — dirs collided")
	}
	if got1.Summary != first.Summary {
		t.Fatalf("ver-1 summary got overwritten: got %q, want %q", got1.Summary, first.Summary)
	}
	if got2.Summary != second.Summary {
		t.Fatalf("ver-2 summary mismatch: got %q, want %q", got2.Summary, second.Summary)
	}
}

func TestWriteActiveFeedback_TruncatesToBudget(t *testing.T) {
	stageDir := t.TempDir()
	verID := "ver-big"

	// Собираем результат с большим количеством крупных блокирующих findings,
	// заведомо превышающим 12 KiB в сериализованном виде.
	findings := make([]verify.Finding, 0, 200)
	bigText := strings.Repeat("подробное описание проблемы с юникодом — «ёлки-палки» ", 40)
	for i := 0; i < 200; i++ {
		findings = append(findings, verify.Finding{
			Blocking:    true,
			Title:       "проблема номер N",
			Requirement: bigText,
			Evidence:    bigText,
			MinimalFix:  bigText,
		})
	}
	r := verify.ModelResult{
		SchemaVersion: 1,
		Verdict:       verify.VerdictNeedsChanges,
		Summary:       "много проблем",
		Findings:      findings,
	}

	if err := WriteReport(stageDir, verID, r, "codex", 1); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	if err := WriteActiveFeedback(stageDir, verID, r, "codex", 1); err != nil {
		t.Fatalf("WriteActiveFeedback: %v", err)
	}

	text, _, ok := LoadActiveFeedback(stageDir)
	if !ok {
		t.Fatal("LoadActiveFeedback: expected ok=true")
	}
	if len(text) > feedbackBudgetBytes {
		t.Fatalf("feedback.md exceeds budget: %d > %d bytes", len(text), feedbackBudgetBytes)
	}
	if !utf8.ValidString(text) {
		t.Fatal("feedback.md is not valid UTF-8 after truncation")
	}
	if !strings.Contains(text, "обрезано") {
		t.Fatalf("feedback.md missing truncation marker; got tail: %q", text[len(text)-200:])
	}

	reportPath := filepath.Join(VerifyDir(stageDir), verID, reportFileName)
	reportData, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report.md: %v", err)
	}
	// В полном отчёте должны быть ВСЕ findings, а не только влезшие в бюджет
	// feedback.md — отчёт на диске всегда полный.
	if strings.Count(string(reportData), bigText) != len(findings)*3 {
		// bigText встречается 3 раза на finding: requirement, evidence, minimal_fix.
		t.Fatalf("report.md looks truncated: expected %d occurrences of bigText, got %d",
			len(findings)*3, strings.Count(string(reportData), bigText))
	}
}

func TestClearActiveFeedback_RemovesFeedbackKeepsArchive(t *testing.T) {
	stageDir := t.TempDir()
	verID := "ver-1"
	r := sampleAcceptedResult()

	if err := WriteReport(stageDir, verID, r, "codex", 1); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	if err := WriteActiveFeedback(stageDir, verID, r, "codex", 1); err != nil {
		t.Fatalf("WriteActiveFeedback: %v", err)
	}
	if err := SaveAcceptedResult(stageDir, verID, 1, r); err != nil {
		t.Fatalf("SaveAcceptedResult: %v", err)
	}

	if err := ClearActiveFeedback(stageDir); err != nil {
		t.Fatalf("ClearActiveFeedback: %v", err)
	}

	if _, _, ok := LoadActiveFeedback(stageDir); ok {
		t.Fatal("LoadActiveFeedback: expected ok=false after ClearActiveFeedback")
	}

	// Архив прохода должен остаться нетронутым.
	reportPath := filepath.Join(VerifyDir(stageDir), verID, reportFileName)
	if _, err := os.Stat(reportPath); err != nil {
		t.Fatalf("report.md should survive ClearActiveFeedback: %v", err)
	}
	resultPath := filepath.Join(StepDir(stageDir, verID, 1), resultFileName)
	if _, err := os.Stat(resultPath); err != nil {
		t.Fatalf("result.json should survive ClearActiveFeedback: %v", err)
	}

	// Повторный ClearActiveFeedback на уже отсутствующем файле — не ошибка.
	if err := ClearActiveFeedback(stageDir); err != nil {
		t.Fatalf("ClearActiveFeedback should be idempotent: %v", err)
	}
}

func TestLoadActiveFeedback_NoFile(t *testing.T) {
	stageDir := t.TempDir()
	if _, _, ok := LoadActiveFeedback(stageDir); ok {
		t.Fatal("LoadActiveFeedback: expected ok=false when verify/feedback.md does not exist")
	}
}

func TestNewVerificationID_InjectableForTests(t *testing.T) {
	original := newVerificationID
	defer func() { newVerificationID = original }()

	newVerificationID = func() string { return "fixed-id-for-tests" }
	if got := NewVerificationID(); got != "fixed-id-for-tests" {
		t.Fatalf("NewVerificationID: got %q, want overridden fixed value", got)
	}
}

// TestReportRelPath_MatchesWriteReportLocation закрывает контракт V5b.3:
// путь, который ReportRelPath отдаёт pkg/server для резолва отчёта через
// os.Root(runDir), должен указывать РОВНО туда, куда WriteReport реально
// пишет report.md — иначе сервер искал бы отчёт не там, где он есть.
func TestReportRelPath_MatchesWriteReportLocation(t *testing.T) {
	stageDir := t.TempDir()
	verID := "v-20260101-020304-abcd"

	if err := WriteReport(stageDir, verID, sampleAcceptedResult(), "codex", 1); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	got := filepath.Join(stageDir, ReportRelPath(verID))
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("ReportRelPath does not point at the file WriteReport created: %v", err)
	}
}

func TestWriteManifest_RoundTrips(t *testing.T) {
	stageDir := t.TempDir()
	verID := "ver-1"
	want := Manifest{
		RunID:          "flow-20260101-abcd",
		StageID:        "build",
		Phase:          "implementation",
		VerificationID: verID,
		CreatedAt:      time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC),
		Steps: []ManifestStep{
			{Index: 1, Kind: "shell", Command: "go test ./...", Outcome: "pass"},
			{Index: 2, Kind: "agent", Command: "codex", Outcome: "needs_changes"},
		},
	}
	if err := WriteManifest(stageDir, verID, want); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	path := filepath.Join(VerifyDir(stageDir), verID, manifestFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	var got Manifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal manifest.json: %v", err)
	}

	if got.RunID != want.RunID || got.StageID != want.StageID || got.Phase != want.Phase ||
		got.VerificationID != want.VerificationID {
		t.Fatalf("manifest metadata mismatch: got %+v, want %+v", got, want)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("CreatedAt mismatch: got %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	if len(got.Steps) != len(want.Steps) {
		t.Fatalf("Steps length mismatch: got %d, want %d", len(got.Steps), len(want.Steps))
	}
	for i := range want.Steps {
		if got.Steps[i] != want.Steps[i] {
			t.Fatalf("Steps[%d] mismatch: got %+v, want %+v", i, got.Steps[i], want.Steps[i])
		}
	}
}
