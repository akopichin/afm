package memorypipeline

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/akopichin/afm/pkg/memory"
)

// Статусы жизненного цикла Manifest — единый источник правды по всей
// цепочке CLI (Task 11) + Pipeline.Finalize (Task 8):
//
//	running -> capturing -> distilling -> {dry_run|cancelled|failed|promoting}
//	promoting -> awaiting_commit | published
//	(CLI) awaiting_commit|published -> completed | failed
//
// Finalize сам НИКОГДА не пишет "completed" — это решение принимает CLI
// после решения про коммит.
const (
	StatusRunning        = "running"
	StatusCapturing      = "capturing"
	StatusDistilling     = "distilling"
	StatusPromoting      = "promoting"
	StatusAwaitingCommit = "awaiting_commit"
	StatusPublished      = "published"
	StatusCompleted      = "completed"
	StatusDryRun         = "dry_run"
	StatusFailed         = "failed"
	StatusCancelled      = "cancelled"
)

// nonTerminalStatuses — статусы, ещё не завершившие текущую попытку. Если
// процесс упал или был прерван, пока манифест в одном из них,
// FinalizeManifestOnExit обязан закрыть его в cancelled/failed, а не
// оставить "зависшим" навсегда.
var nonTerminalStatuses = map[string]bool{
	StatusRunning:    true,
	StatusCapturing:  true,
	StatusDistilling: true,
	StatusPromoting:  true,
}

// Manifest — персистентное состояние одной попытки memory-rebuild:
// метаданные операции (OperationMeta) плюс исход (Status/FinishedAt/
// Datasets/Targets/Errors/Commit*). Пишется атомарно на каждый переход
// статуса, чтобы прерванный процесс можно было диагностировать и
// восстановить по последнему manifest.json на диске.
type Manifest struct {
	OperationMeta

	Status     string `json:"status"`
	FinishedAt string `json:"finished_at,omitempty"`

	Datasets []DatasetResult `json:"datasets,omitempty"`
	Targets  []TargetResult  `json:"targets,omitempty"`
	Errors   []string        `json:"errors,omitempty"`

	CommitCreated bool   `json:"commit_created"`
	CommitSHA     string `json:"commit_sha,omitempty"`
	CommitError   string `json:"commit_error,omitempty"`
}

// WriteManifest сериализует m в JSON и атомарно+durable записывает в path
// (через memory.AtomicWrite: temp+fsync+rename+fsync родителя).
func WriteManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return memory.AtomicWrite(path, data)
}

// LoadManifest читает и разбирает manifest.json по пути path.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// NewRunningManifest создаёт манифест только что начатой попытки: статус
// running + полные метаданные операции. Вызывается CLI (Task 11) сразу
// после того, как аллоцирована директория попытки (NewUniqueAttemptDir).
func NewRunningManifest(meta OperationMeta) *Manifest {
	return &Manifest{
		OperationMeta: meta,
		Status:        StatusRunning,
	}
}

// FinalizeManifestOnExit — defer-хелпер для CLI (Task 11):
//
//	defer FinalizeManifestOnExit(manifestPath, &err, ctx)
//
// Если на диске манифест всё ещё в нетерминальном статусе (процесс упал или
// был прерван где-то в середине конвейера), переводит его в cancelled (если
// ctx отменён) либо failed (иначе), дописывает finalErr (если он ненулевой)
// в Errors и проставляет FinishedAt. Уже терминальный манифест
// (dry_run/promoting-produced published/awaiting_commit/completed/failed/
// cancelled) не трогается — Finalize/CLI уже довели его до финала сами.
//
// Ошибка чтения/записи манифеста внутри этого хелпера намеренно
// проглатывается: это последний defer в цепочке завершения процесса, и
// поднимать вторую ошибку поверх исходной здесь уже некому.
func FinalizeManifestOnExit(path string, finalErr *error, ctx context.Context) { //nolint:revive // deliberate defer-friendly signature: defer FinalizeManifestOnExit(path, &err, ctx) reads left-to-right at the call site
	m, err := LoadManifest(path)
	if err != nil {
		return
	}
	if !nonTerminalStatuses[m.Status] {
		return
	}

	if ctx.Err() != nil {
		m.Status = StatusCancelled
	} else {
		m.Status = StatusFailed
	}
	if finalErr != nil && *finalErr != nil {
		m.Errors = append(m.Errors, (*finalErr).Error())
	}
	m.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	_ = WriteManifest(path, m)
}

// MarkManifestCompleted переводит манифест в терминальный статус completed.
// Вызывается CLI ПОСЛЕ решения о коммите (Finalize сам "completed" никогда
// не пишет — см. описание состояний выше).
func MarkManifestCompleted(path string) error {
	m, err := LoadManifest(path)
	if err != nil {
		return err
	}
	m.Status = StatusCompleted
	m.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	return WriteManifest(path, m)
}
