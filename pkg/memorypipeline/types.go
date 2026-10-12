// Package memorypipeline builds prompts and runs fresh agents for memory extraction; the orchestrator owns
// scheduling.
package memorypipeline

import (
	"context"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
)

// Kind* — значения AgentSpec.Kind, общие с switch в BuildPrompt (ниже) и с конвейером в
// pkg/orchestrator/reflection.go — единые константы, а не разбросанные строковые литералы (goconst).
const (
	KindReflect    = "reflect"
	KindAggregate  = "aggregate"
	KindPrioritize = "prioritize"
	KindUpdate     = "update"
)

// Phase* — accounting phase labels for one memory-pipeline agent call, NOT to be confused with Kind*.
const (
	PhaseReflect    = "memory_reflect"
	PhaseAggregate  = "memory_aggregate"
	PhasePrioritize = "memory_prioritize"
	PhaseUpdate     = "memory_update"
)

// ScopeRunOverhead — AgentSpec.Scope for aggregate/prioritize/update: these steps distill datasets from
// potentially many stages, so they are never attributed to one stage.
const ScopeRunOverhead = accounting.ScopeRunOverhead

// Prompts holds the compiled base templates for each memory-pipeline step.
type Prompts struct {
	Reflect    string
	Aggregate  string
	Prioritize string
	Update     string
}

// AgentConfig задаёт команду, wrapper, рабочие каталоги, idle-таймаут и debug для всех шагов памяти.
type AgentConfig struct {
	Command     string
	ExtraArgs   []string
	WrapperDir  string
	RootDir     string
	RunDir      string
	IdleTimeout time.Duration
	Debug       bool
	// OnUsage, if set, builds the executor.Config.OnUsage callback for one agent call given its attribution —
	// the exact signature of.recordUsage, which is what production wires in.
	OnUsage func(stageID, phase, scope string) func(accounting.Observation)
}

// AgentSpec — единый параметр для запуска одного агента конвейера памяти. Заполняются только поля,
// релевантные Kind.
type AgentSpec struct {
	Kind      string // "reflect" | "aggregate" | "prioritize" | "update"
	StageName string // для лога/имени
	LogFile   string // абс. путь к логу этого агента

	// StageID/Phase/Scope attribute this one agent call to the accounting ledger: reflect is attributed to
	// the source stage; aggregate/ prioritize/update are attributed to the run as a whole.
	StageID string
	Phase   string
	Scope   string

	// reflect:
	Sources    []string // абс. пути (файлы или директории) для чтения
	DatasetOut string   // абс. путь, куда записать YAML-датасет (project_level/session_level)

	// aggregate: InPaths = датасет-файлы, Out = абс. путь для patterns.md.
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
