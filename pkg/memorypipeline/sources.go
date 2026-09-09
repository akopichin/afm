package memorypipeline

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
)

// Inventory — классификация реальных файлов stage-директории, которые может
// прочитать reflect-агент: AgentSessions (логи/стрим агента, планы,
// summary, диалог) против Supplemental (заметки пользователя, hook-логи).
// Разделение важно потому, что скрипт-стадия (before.log/after.log/
// script.log, без единого запуска агента) не должна считаться имеющей
// агентскую сессию — см. HasAgentSession (review #9).
type Inventory struct {
	AgentSessions []string
	Supplemental  []string
}

// All возвращает источники в порядке: сначала агентская сессия, затем
// supplemental — это намеренный, детерминированный порядок (обе группы уже
// отсортированы внутри себя), чтобы reflect-промпт видел основной материал
// сессии раньше заметок/hook-логов (review #12).
func (i Inventory) All() []string {
	return append(append([]string{}, i.AgentSessions...), i.Supplemental...)
}

// HasAgentSession сообщает, была ли у стадии хотя бы одна реальная
// агентская сессия (в отличие от чисто скриптовой стадии).
func (i Inventory) HasAgentSession() bool {
	return len(i.AgentSessions) > 0
}

// agentSessionNames строит множество имён файлов агентской сессии из
// единого источника правды pkg/flow (Phases/PhaseLogFiles/PhaseStreamLogs) —
// чтобы список не расходился с реальными именами, которые пишет executor.
func agentSessionNames() map[string]bool {
	m := map[string]bool{"plan.md": true, "execution_summary.md": true}
	for _, p := range flow.Phases() {
		for _, f := range flow.PhaseLogFiles(p) {
			m[f] = true
			m[strings.TrimSuffix(f, ".log")+".stderr.log"] = true
		}
		for _, f := range flow.PhaseStreamLogs(p) {
			m[f] = true
		}
	}
	return m
}

// supplementalNames — файлы прямого пользовательского ввода и hook-логи:
// относятся к сессии стадии, но не являются логом запуска самого агента.
var supplementalNames = map[string]bool{
	"prenote.md":        true,
	"feedback.md":       true,
	"before.log":        true,
	"after.log":         true,
	"before.stderr.log": true,
	"after.stderr.log":  true,
}

// planVersionRe matches historical plan versions (state.VersionPlan renames
// plan.md → plan.v1.md, plan.v2.md, …).
var planVersionRe = regexp.MustCompile(`^plan\.v\d+\.md$`)

// isDialog matches the file-based dialog protocol's per-question files
// (question/answer/history) regardless of phase prefix.
func isDialog(n string) bool {
	return strings.HasSuffix(n, ".dialog.jsonl") ||
		strings.HasSuffix(n, ".question.json") ||
		strings.HasSuffix(n, ".answer.json")
}

// artifactPrefixes — имена, которые сама memory-пайплайна пишет в
// stage-директорию (reflect_dataset.yaml и промежуточные *.md шагов
// aggregate/prioritize/update). Их нельзя скармливать reflect-агенту как
// исходник — иначе конвейер читал бы собственный вывод предыдущих прогонов.
var artifactPrefixes = []string{"reflect", "aggregate", "prioritize", "update", "patterns", "prioritized", "high"}

func isArtifact(n string) bool {
	if n == "reflect_dataset.yaml" {
		return true
	}
	for _, p := range artifactPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// SourceInventory классифицирует реальные файлы в stageDir на AgentSessions
// и Supplemental для reflect-шага memory-пайплайны. Пути в результате —
// абсолютные, каждая группа отсортирована. Отсутствующая stageDir — не
// ошибка (пустой Inventory); любая другая ошибка чтения директории или
// Lstat конкретной записи возвращается вызывающему коду как есть — путь
// строгий, без сети, молча проглатывать такие ошибки нельзя (review #9).
func SourceInventory(stageDir string) (Inventory, error) {
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		if os.IsNotExist(err) {
			return Inventory{}, nil
		}
		return Inventory{}, err
	}
	abs, err := filepath.Abs(stageDir)
	if err != nil {
		return Inventory{}, err
	}
	sess := agentSessionNames()
	var inv Inventory
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || isArtifact(n) {
			continue
		}
		info, err := os.Lstat(filepath.Join(stageDir, n))
		if err != nil {
			return Inventory{}, err // do not swallow (review #9)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		full := filepath.Join(abs, n)
		switch {
		case sess[n] || isDialog(n) || planVersionRe.MatchString(n):
			inv.AgentSessions = append(inv.AgentSessions, full)
		case supplementalNames[n]:
			inv.Supplemental = append(inv.Supplemental, full)
		default:
			// Неопознанный файл (например, script.log скрипт-стадии) — не
			// источник для reflect-агента, пропускаем.
		}
	}
	slices.Sort(inv.AgentSessions)
	slices.Sort(inv.Supplemental)
	return inv, nil
}
