// Package memorypipeline holds the reusable engine behind afm's agent-memory
// v3 pipeline: prompt templates, per-step agent specs, and the exec-backed
// runner that spawns a fresh-context agent for one step (reflect/aggregate/
// prioritize/update). The orchestrator package owns scheduling (when to run
// which step, serialization, best-effort notices); this package only knows
// how to build a prompt and run one agent for a given spec.
package memorypipeline

import (
	"context"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
)

// Kind* — значения AgentSpec.Kind, общие с switch в BuildPrompt (ниже) и с
// конвейером в pkg/orchestrator/reflection.go — единые константы, а не
// разбросанные строковые литералы (goconst).
const (
	KindReflect    = "reflect"
	KindAggregate  = "aggregate"
	KindPrioritize = "prioritize"
	KindUpdate     = "update"
)

// Phase* — accounting phase labels for one memory-pipeline agent call (the
// `phase` argument to accounting.Store.Append/executor.Config.Phase), NOT to
// be confused with Kind* (the pipeline step name). Reflect is attributed to
// the source stage; the other three steps run once per end-of-run pass over
// ALL stages together, so they're attributed to the run as a whole via
// ScopeRunOverhead (see AgentSpec.Scope) rather than any single stage.
const (
	PhaseReflect    = "memory_reflect"
	PhaseAggregate  = "memory_aggregate"
	PhasePrioritize = "memory_prioritize"
	PhaseUpdate     = "memory_update"
)

// ScopeRunOverhead — AgentSpec.Scope for aggregate/prioritize/update: these
// steps distill datasets from potentially many stages (or the whole run's
// project-wide memory), so they are never attributed to one stage (StageID
// stays ""). Aliases accounting.ScopeRunOverhead (the same literal the
// accounting ledger already uses to exclude run-level records from
// Ledger.SummaryByStage) rather than redeclaring the string, so the two
// packages can never drift apart.
const ScopeRunOverhead = accounting.ScopeRunOverhead

// Prompts holds the compiled base templates for each memory-pipeline step.
type Prompts struct {
	Reflect    string
	Aggregate  string
	Prioritize string
	Update     string
}

// AgentConfig — параметры запуска агента конвейера памяти, общие для всех
// шагов (не зависят от конкретного spec): какую команду запускать по
// умолчанию, где искать generated-враппер, рабочую директорию/директорию
// рана, таймаут простоя и debug-логирование.
type AgentConfig struct {
	Command     string
	ExtraArgs   []string
	WrapperDir  string
	RootDir     string
	RunDir      string
	IdleTimeout time.Duration
	Debug       bool
	// OnUsage, if set, builds the executor.Config.OnUsage callback for one
	// agent call given its (StageID, Phase, Scope) attribution — the exact
	// signature of (*orchestrator.Orchestrator).recordUsage, which is what
	// production wires in (see pkg/orchestrator/orchestrator.go's New). nil
	// disables usage recording for the whole pipeline (accounting off).
	OnUsage func(stageID, phase, scope string) func(accounting.Observation)
}

// AgentSpec — единый параметр для запуска одного агента конвейера памяти.
// Заполняются только поля, релевантные Kind. Один seam (AgentRunner)
// принимает этот spec — так тесты подменяют реальный запуск процесса.
type AgentSpec struct {
	Kind      string // "reflect" | "aggregate" | "prioritize" | "update"
	StageName string // для лога/имени
	LogFile   string // абс. путь к логу этого агента

	// StageID/Phase/Scope attribute this one agent call to the accounting
	// ledger (see AgentConfig.OnUsage): reflect is attributed to the source
	// stage (StageID=<stage id>, Phase=PhaseReflect, Scope=""); aggregate/
	// prioritize/update are attributed to the run as a whole (StageID="",
	// Scope=ScopeRunOverhead, Phase=PhaseAggregate/PhasePrioritize/PhaseUpdate).
	StageID string
	Phase   string
	Scope   string

	// reflect:
	Sources    []string // абс. пути (файлы или директории) для чтения
	DatasetOut string   // абс. путь, куда записать YAML-датасет (project_level/session_level)

	// aggregate: InPaths = датасет-файлы (reflect_dataset.yaml, один или
	// несколько за end-of-run проход), Out = абс. путь для patterns.md.
	// prioritize: In = patterns.md, Out = абс. путь для prioritized.md.
	// (одни и те же поля переиспользуются между aggregate и prioritize — так
	// проще, чем заводить по паре полей на каждый шаг ради симметрии.)
	InPaths []string
	In      string
	Out     string

	// update:
	HighPath   string // абс. путь к high.md (High-паттерны, отобранные кодом)
	TargetFile string // абс. путь к файлу памяти, который нужно переписать
	MaxRules   int    // предел количества паттернов в TargetFile
}

// AgentRunner runs one memory-pipeline agent for the given spec. The
// production implementation is NewExecRunner; tests inject a stub.
type AgentRunner func(ctx context.Context, spec AgentSpec) error
