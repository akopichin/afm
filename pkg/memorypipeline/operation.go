package memorypipeline

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// OperationMeta — метаданные одного запуска memory-rebuild конвейера,
// зафиксированные ДО начала работы вызывающим кодом (CLI, Task 11).
// StartedAt задаётся явно (не time.Now() внутри пакета), чтобы тесты
// оставались детерминированными.
type OperationMeta struct {
	RunID      string `json:"run_id"`
	RunPath    string `json:"run_path"`
	FlowPath   string `json:"flow_path"`
	FlowSHA256 string `json:"flow_sha256"`
	RootDir    string `json:"root_dir"`
	MemoryDir  string `json:"memory_dir"`
	MemoryMode string `json:"memory_mode"`
	MaxRules   int    `json:"max_rules"`
	// EffectiveCommit — итоговое решение "коммитить ли после публикации"
	// (флаг + flow.yaml + окружение уже разрешены вызывающим кодом в одно
	// булево значение).
	EffectiveCommit bool `json:"effective_commit"`
	// PromptsDir — источник промптов: путь override-директории, либо ""
	// (встроенные по умолчанию промпты, review #2).
	PromptsDir string `json:"prompts_dir,omitempty"`
	// PromptSHA256 — SHA-256 текста промптов reflect/aggregate/prioritize/
	// update, использованных в этом запуске (для аудита, что конкретно
	// сгенерировало данный результат).
	PromptSHA256 map[string]string `json:"prompt_sha256,omitempty"`
	// StartedAt — RFC3339, задаётся вызывающим кодом.
	StartedAt string `json:"started_at"`
}

// StepError — типизированная ошибка одного шага конвейера (review
// #3.minor): какая стадия, какая цель (для финализирующих шагов), какой шаг
// и где лежит лог этого агента — вместо голого fmt.Errorf со строкой,
// которую пришлось бы парсить, чтобы понять, что именно упало.
type StepError struct {
	StageID string
	Target  string
	Step    string
	LogPath string
	Err     error
}

func (e *StepError) Error() string {
	return fmt.Sprintf("memory pipeline step %q failed (stage=%q target=%q log=%q): %v",
		e.Step, e.StageID, e.Target, e.LogPath, e.Err)
}

func (e *StepError) Unwrap() error { return e.Err }

// Значения поля DatasetResult.Source.
const (
	// SourceReused — переиспользован уже существующий канонический
	// reflect_dataset.yaml стадии.
	SourceReused = "reused"
	// SourceGenerated — датасет сгенерирован заново и ждёт публикации.
	SourceGenerated = "generated"
)

// DatasetResult — итог обработки reflect_dataset.yaml одной стадии: либо
// переиспользован уже существующий канонический файл (SourceReused), либо
// сгенерирован заново и ждёт публикации (SourceGenerated) — см. review #2/#8.
type DatasetResult struct {
	StageID     string `json:"stage_id"`
	Source      string `json:"source"` // SourceReused | SourceGenerated
	DatasetPath string `json:"dataset_path"`
	// SourceFiles/SourceHashes — входные файлы, которые читал reflect-агент,
	// и их SHA-256, позиционно выровненные (SourceHashes[i] — хэш
	// SourceFiles[i]).
	SourceFiles   []string `json:"source_files,omitempty"`
	SourceHashes  []string `json:"source_hashes,omitempty"`
	DatasetSHA256 string   `json:"dataset_sha256"`
	// CanonicalPath — <RunDir>/<stageID>/reflect_dataset.yaml, публикационная
	// цель для сгенерированного датасета.
	CanonicalPath string `json:"canonical_path"`
	// Changed — для сгенерированного датасета: отличаются ли байты кандидата
	// (staging) от канонического файла (Finalize step 4). Только Changed
	// датасеты публикуются в CanonicalPath.
	Changed bool `json:"changed,omitempty"`
	// Published — выставляется, когда сгенерированный датасет опубликован
	// (переименован) в CanonicalPath.
	Published bool `json:"published"`
}

// TargetResult — итог сборки одного целевого файла памяти (общий
// project-level memory.md либо файл конкретной стадии).
type TargetResult struct {
	Label         string `json:"label"`
	FinalPath     string `json:"final_path"`
	CandidatePath string `json:"candidate_path,omitempty"`
	OldSHA256     string `json:"old_sha256,omitempty"`
	NewSHA256     string `json:"new_sha256,omitempty"`
	Changed       bool   `json:"changed"`
	NoHigh        bool   `json:"no_high"`
	Published     bool   `json:"published"`
	// Diff — unified diff old->candidate, заполняется только для изменённых
	// целей.
	Diff string `json:"diff,omitempty"`
}

// Report — сводка одного запуска конвейера, возвращаемая вызывающему коду
// (CLI, Task 11) для показа пользователю.
type Report struct {
	RunID    string          `json:"run_id"`
	WorkDir  string          `json:"work_dir"`
	Datasets []DatasetResult `json:"datasets"`
	Targets  []TargetResult  `json:"targets"`
}

// maxAttemptDirRetries — сколько раз NewUniqueAttemptDir пробует новый id
// от gen(), прежде чем сдаться.
const maxAttemptDirRetries = 8

// NewUniqueAttemptDir создаёт устойчивую родительскую директорию base
// (MkdirAll — review #9: самый первый rebuild не должен падать с ENOENT
// из-за отсутствующей <run>/memory-rebuild), затем пытается эксклюзивно
// создать дочернюю директорию через os.Mkdir с именем от gen(). При
// коллизии (EEXIST) повторяет с новым id, не более maxAttemptDirRetries раз.
func NewUniqueAttemptDir(base string, gen func() string) (string, error) {
	if err := os.MkdirAll(base, 0755); err != nil {
		return "", err
	}
	for i := 0; i < maxAttemptDirRetries; i++ {
		dir := filepath.Join(base, gen())
		err := os.Mkdir(dir, 0755)
		if err == nil {
			return dir, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("could not allocate a unique attempt dir under %s after %d tries", base, maxAttemptDirRetries)
}

// ScanUnfinishedPromotions возвращает директории попыток под
// <runDir>/memory-rebuild, чей manifest.json всё ещё в статусе "promoting" —
// т.е. предыдущий запуск упал или был прерван ровно во время публикации
// файлов (review #6).
//
// Область поиска — намеренно только этот run (review #12 в брифе): "чужой"
// promoting от другого run'а, пишущего в тот же memory.path, здесь не
// всплывёт. Это допустимо — повторный rebuild чисто пересобирается из
// канонического/attempt-состояния, а конкурентную публикацию исключает
// общая блокировка памяти.
func ScanUnfinishedPromotions(runDir string) []string {
	pattern := filepath.Join(runDir, "memory-rebuild", "*", "manifest.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	var out []string
	for _, mf := range matches {
		m, err := LoadManifest(mf)
		if err != nil {
			continue
		}
		if m.Status == StatusPromoting {
			out = append(out, filepath.Dir(mf))
		}
	}
	return out
}
