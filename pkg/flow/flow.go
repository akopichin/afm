package flow

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// AgentType defines which built-in agents a stage uses.
type AgentType string

const (
	AgentPlanning       AgentType = "planning"
	AgentImplementation AgentType = "implementation"
	AgentReview         AgentType = "review"
	// AgentAuto — псевдо-агент: стадия исполняется автономным агентом напрямую,
	// без supervisor/LLM-решения и без фолбэка. Должен быть единственным агентом.
	AgentAuto AgentType = "auto"
)

// Artifact describes a file that a stage produces for other stages.
type Artifact struct {
	Name        string `yaml:"name"`
	Path        string `yaml:"path"`
	Description string `yaml:"description,omitempty"`
	Inline      *bool  `yaml:"inline,omitempty"`
}

// IsInline returns whether the artifact content should be inlined into the prompt.
// Defaults to true when Inline is nil.
func (a Artifact) IsInline() bool {
	return a.Inline == nil || *a.Inline
}

// Input describes an artifact that a stage consumes from a dependency.
// Supports unmarshalling from a plain string "stage.artifact" or an object {ref, optional}.
type Input struct {
	Ref      string `yaml:"ref"`
	Optional bool   `yaml:"optional,omitempty"`
}

// UnmarshalYAML allows Input to be parsed from a string or an object.
func (inp *Input) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		inp.Ref = value.Value
		return nil
	}
	type plain Input
	return value.Decode((*plain)(inp))
}

// Reflect — конфиг отражения (reflection) стадии для agent-памяти v3.
// File — путь (относительно memory.path) к ЦЕЛЕВОМУ файлу памяти этой стадии:
// шаг update конвейера пишет туда смёрженные High-паттерны, а Mode с CanRead()
// инъецирует его как указатель в промпт стадии; обязателен.
// Mode — r|w|rw, режим доступа агентов к памяти (0 → по умолчанию rw).
type Reflect struct {
	File string `yaml:"file"`
	Mode string `yaml:"mode,omitempty"`
}

// Mode constants for Reflect.
const (
	ReflectModeR  = "r"
	ReflectModeW  = "w"
	ReflectModeRW = "rw"
)

// CanRead reports whether the reflect mode allows reading.
func (r *Reflect) CanRead() bool {
	return r.Mode == ReflectModeR || r.Mode == ReflectModeRW
}

// CanWrite reports whether the reflect mode allows writing.
func (r *Reflect) CanWrite() bool {
	return r.Mode == ReflectModeW || r.Mode == ReflectModeRW
}

// Button — предопределённая кнопка кебаб-меню стадии: Label — и отображаемая
// подпись, и ключ поиска; Prompt — инструкция, которая доставляется живому
// агенту через Revise при клике.
type Button struct {
	Label  string
	Prompt string
}

// Buttons сохраняет порядок объявления кнопок из YAML (плоский
// map[string]string итерировался бы случайно, тасуя меню). Декодируется из
// mapping-узла обходом value.Content попарно.
type Buttons []Button

// UnmarshalYAML декодирует Buttons из YAML-mapping'а label: prompt, сохраняя
// порядок объявления (зеркалит Input.UnmarshalYAML — обход value.Content).
func (b *Buttons) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return errors.New("buttons must be a mapping of label: prompt")
	}
	out := make(Buttons, 0, len(value.Content)/2)
	for i := 0; i+1 < len(value.Content); i += 2 {
		out = append(out, Button{
			Label:  value.Content[i].Value,
			Prompt: value.Content[i+1].Value,
		})
	}
	*b = out
	return nil
}

// Prompt возвращает prompt для кнопки с данной подписью, или "" если такой
// кнопки нет.
func (b Buttons) Prompt(label string) string {
	for _, btn := range b {
		if btn.Label == label {
			return btn.Prompt
		}
	}
	return ""
}

// Labels возвращает подписи кнопок в порядке объявления.
func (b Buttons) Labels() []string {
	out := make([]string, len(b))
	for i, btn := range b {
		out[i] = btn.Label
	}
	return out
}

// Stage represents a single stage in a flow.
type Stage struct {
	ID          string      `yaml:"id"`
	Name        string      `yaml:"name"`
	Description string      `yaml:"description,omitempty"`
	Agents      []AgentType `yaml:"agents,omitempty,flow"`
	Skills      []string    `yaml:"skills,omitempty,flow"`
	DependsOn   []string    `yaml:"depends_on,omitempty,flow"`
	// EagerPlanning starts the planning agent immediately without
	// waiting for depends_on stages to finish (the default before
	// the planning-by-deps gate was introduced).
	EagerPlanning bool `yaml:"eager_planning,omitempty"`
	// Plan is an optional path to an existing plan file.
	// If set, the planning agent is skipped.
	Plan string `yaml:"plan,omitempty"`
	// Command overrides the global client command for this stage.
	Command string `yaml:"command,omitempty"`
	// MaxParallel limits concurrent stages using the same command.
	MaxParallel int        `yaml:"max_parallel,omitempty"`
	Artifacts   []Artifact `yaml:"artifacts,omitempty"`
	Inputs      []Input    `yaml:"inputs,omitempty"`
	Interactive bool       `yaml:"interactive,omitempty"`
	// Verify is an optional shell command executed in the project directory
	// after the stage reports completion. Non-zero exit means the stage is
	// not actually done, regardless of what the agent claims.
	Verify string `yaml:"verify,omitempty"`
	// Prompt is an optional explicit instruction delivered to the agent
	// after the <stage> context block.
	Prompt string `yaml:"prompt,omitempty"`
	// Script, if set, makes this a script-only stage: it runs the given shell
	// script (via sh -c) instead of any agent, with no planning/approval gate.
	// Mutually exclusive with Agents/Command/Interactive/Plan/Verify.
	Script        string        `yaml:"script,omitempty"`
	ScriptTimeout time.Duration `yaml:"script_timeout,omitempty"`
	// ScriptBefore/ScriptAfter run a shell script immediately before/after this
	// stage's own main content (agent, script, or interactive). Legal on any
	// stage type, alongside its other fields.
	ScriptBefore        string        `yaml:"script_before,omitempty"`
	ScriptBeforeTimeout time.Duration `yaml:"script_before_timeout,omitempty"`
	ScriptAfter         string        `yaml:"script_after,omitempty"`
	ScriptAfterTimeout  time.Duration `yaml:"script_after_timeout,omitempty"`
	// AutoApprove, if true, approves this stage's plan automatically the
	// instant it's ready (awaiting_approval), with no human interaction —
	// regardless of whether a dashboard is attached and regardless of
	// --require-approval. Default false. Intended for CI runs where some
	// stages need human review and others don't.
	AutoApprove bool `yaml:"auto_approve,omitempty"`
	// AutoRun, если явно false, приостанавливает стадию сразу при первой
	// активации (когда её depends_on выполнены) вместо немедленного старта —
	// стадия уходит в paused с PausedFrom=pending и ждёт Continue. nil (не
	// задано) или true — прежнее поведение, немедленный старт. Гейт
	// срабатывает один раз: см. state.StageState.PausedFrom.
	AutoRun *bool `yaml:"auto_run,omitempty"`
	// Buttons — предопределённые пункты кебаб-меню этой стадии: label → prompt.
	// Клик доставляет prompt живому агенту через Revise (тот же путь, что и
	// свободная заметка). Порядок сохраняется из YAML. Недопустимо на скриптовой
	// стадии (нет агента). См.
	// docs/superpowers/specs/2026-08-27-stage-custom-buttons-design.md.
	Buttons Buttons `yaml:"buttons,omitempty"`
	// Reflect (v3): конфиг отражения этой стадии для agent-памяти.
	// nil (не задано) — reflect отключен. Непусто требует непустой memory.path на уровне флоу.
	// На script-стадии допустимо, но во время выполнения тихо пропускается (нет
	// агентской сессии). Обязателен reflect.file; reflect.mode опционален (дефолт rw).
	Reflect *Reflect `yaml:"reflect,omitempty"`
}

// isBuiltIn reports whether the agent type is one of the three built-in phases.
func isBuiltIn(a AgentType) bool {
	return a == AgentPlanning || a == AgentImplementation || a == AgentReview
}

// HasAgent reports whether the stage uses a specific agent type.
// For AgentImplementation, any custom (non-built-in) agent also counts.
func (s *Stage) HasAgent(a AgentType) bool {
	if s.IsAuto() {
		return false // auto-стадия не имеет planning/implementation/review-агентов
	}
	for _, ag := range s.Agents {
		if ag == a {
			return true
		}
	}
	// Custom agents (e.g. "senior-go-architect") count as implementation.
	if a == AgentImplementation {
		for _, ag := range s.Agents {
			if !isBuiltIn(ag) {
				return true
			}
		}
	}
	return false
}

// ImplAgent returns the agent type used for the implementation phase.
// Custom agents take priority; falls back to AgentImplementation.
func (s *Stage) ImplAgent() AgentType {
	if s.IsAuto() {
		return AgentImplementation // defensive: auto не исполняется как implementation-команда
	}
	for _, ag := range s.Agents {
		if !isBuiltIn(ag) {
			return ag
		}
	}
	return AgentImplementation
}

// NeedsPlanning reports whether a planning agent will run for this stage.
func (s *Stage) NeedsPlanning() bool {
	return s.Plan == "" && s.HasAgent(AgentPlanning)
}

// IsAuto сообщает, что стадия жёстко помечена автономной (agents: [auto]).
func (s *Stage) IsAuto() bool {
	return len(s.Agents) == 1 && s.Agents[0] == AgentAuto
}

// IsScript reports whether the stage runs a plain shell script instead of an
// agent (agents: [] entirely absent, replaced by the Script field).
func (s *Stage) IsScript() bool {
	return s.Script != ""
}

// AutoRunDisabled reports whether this stage's first activation should pause
// instead of starting immediately (auto_run explicitly set to false).
func (s Stage) AutoRunDisabled() bool {
	return s.AutoRun != nil && !*s.AutoRun
}

// MemoryConfig — настройки agent-памяти флоу (v3 — директория).
// Path непустой включает всю фичу (см. docs/superpowers/specs/2026-08-28-agent-memory-v3-design.md).
type MemoryConfig struct {
	// Path — директория для хранения памяти (PROJECT_MEMORY.yaml, SESSION_MEMORY.yaml),
	// относительно root_dir. Непусто = фича включена.
	Path string `yaml:"path,omitempty"`
	// MaxRules — максимальное количество findings для сохранения в памяти per-scope.
	// 0 → дефолт 25 (ставится в ParseFile).
	MaxRules int `yaml:"max_rules,omitempty"`
	// Commit — коммитить ли изменения памяти в git (опционально).
	Commit bool `yaml:"commit,omitempty"`
}

// Flow is the top-level structure parsed from a flow YAML file.
type Flow struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// Prompt is shared text added to the system prompt of every stage and
	// every phase (planning/implementation/review). Empty value does not
	// change behavior.
	Prompt      string `yaml:"prompt,omitempty"`
	MaxParallel int    `yaml:"max_parallel,omitempty"`
	// RootDir — корень проекта, в котором выполняются агенты (их CWD). Нужен,
	// когда рабочая директория процесса afm не совпадает с корнем проекта
	// (напр. Docker-сетап: исходники в /workspace, а .afm — в другом каталоге).
	// Без него агенты наследуют CWD afm и резолвят относительные пути проекта
	// (docs/arch и т.п.) в чужом корне. Пусто → поведение не меняется.
	RootDir string       `yaml:"root_dir,omitempty"`
	Memory  MemoryConfig `yaml:"memory,omitempty"`
	Stages  []Stage      `yaml:"stages"`
}

// MemoryEnabled сообщает, включена ли agent-память для этого флоу.
func (f *Flow) MemoryEnabled() bool { return f.Memory.Path != "" }

// ParseFile reads and validates a flow YAML file.
func ParseFile(path string) (*Flow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read flow file: %w", err)
	}
	var f Flow
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	f.applyScriptTimeoutDefaults()
	if f.MemoryEnabled() {
		if f.Memory.MaxRules == 0 {
			f.Memory.MaxRules = 25
		}
		// Set default reflect mode to "rw" for any stage with Reflect not nil
		for i := range f.Stages {
			s := &f.Stages[i]
			if s.Reflect != nil && s.Reflect.Mode == "" {
				s.Reflect.Mode = "rw"
			}
		}
	}
	warnDeprecatedSupervisorFields(data, path)
	return &f, nil
}

// deprecatedSupervisorStageProbe holds just the removed per-stage
// LLM-supervisor keys, decoded by warnDeprecatedSupervisorFields.
type deprecatedSupervisorStageProbe struct {
	ID               string  `yaml:"id"`
	Supervisor       *bool   `yaml:"supervisor"`
	SupervisorPrompt *string `yaml:"supervisor_prompt"`
}

// warnDeprecatedSupervisorFields best-effort re-parses the raw YAML looking
// for the removed LLM-supervisor keys (supervisor/supervisor_prompt on a
// stage; supervisor_command at the flow root) and prints a non-fatal WARN to
// stderr naming the affected stage id and key. The LLM supervisor was
// removed (agents: [auto] replaces it for autonomous stages); ParseFile's
// primary decode silently drops these now-unknown keys (yaml.Unmarshal isn't
// KnownFields-strict), so a flow.yaml built for the old supervisor keeps
// working exactly as it does today when the supervisor is unconfigured —
// this only makes that behavior change visible instead of silent.
func warnDeprecatedSupervisorFields(data []byte, path string) {
	var probe struct {
		SupervisorCommand *string                          `yaml:"supervisor_command"`
		Stages            []deprecatedSupervisorStageProbe `yaml:"stages"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return
	}
	if probe.SupervisorCommand != nil {
		fmt.Fprintf(os.Stderr, "WARN: %s: flow-level \"supervisor_command\" is no longer supported and is ignored (the LLM supervisor was removed; use \"agents: [auto]\" for autonomous stages)\n", path)
	}
	for _, s := range probe.Stages {
		if s.Supervisor != nil {
			fmt.Fprintf(os.Stderr, "WARN: %s: stage %q: \"supervisor\" is no longer supported and is ignored (use \"agents: [auto]\" for autonomous stages)\n", path, s.ID)
		}
		if s.SupervisorPrompt != nil {
			fmt.Fprintf(os.Stderr, "WARN: %s: stage %q: \"supervisor_prompt\" is no longer supported and is ignored\n", path, s.ID)
		}
	}
}

// defaultScriptTimeout is applied to script/script_before/script_after when
// the corresponding *_timeout field is left unset (zero value) in YAML — the
// documented 300s (5min) default bound against a hung or noisily-looping
// script that would otherwise only be caught by the 24h idle timeout.
const defaultScriptTimeout = 5 * time.Minute

// applyScriptTimeoutDefaults fills in defaultScriptTimeout for any script
// field that is set but whose timeout was left at the Go zero value. An
// explicit timeout in YAML (any non-zero duration) is never overridden.
// Mutates f.Stages in place — f.Stages is a []Stage value slice, so the loop
// indexes into it directly rather than ranging over a copy.
func (f *Flow) applyScriptTimeoutDefaults() {
	for i := range f.Stages {
		s := &f.Stages[i]
		if s.Script != "" && s.ScriptTimeout == 0 {
			s.ScriptTimeout = defaultScriptTimeout
		}
		if s.ScriptBefore != "" && s.ScriptBeforeTimeout == 0 {
			s.ScriptBeforeTimeout = defaultScriptTimeout
		}
		if s.ScriptAfter != "" && s.ScriptAfterTimeout == 0 {
			s.ScriptAfterTimeout = defaultScriptTimeout
		}
	}
}

func (f *Flow) validate() error {
	ids := make(map[string]bool, len(f.Stages))
	for _, s := range f.Stages {
		if ids[s.ID] {
			return fmt.Errorf("duplicate stage id: %q", s.ID)
		}
		ids[s.ID] = true
	}

	for _, s := range f.Stages {
		for _, dep := range s.DependsOn {
			if !ids[dep] {
				return fmt.Errorf("stage %q depends_on unknown stage %q", s.ID, dep)
			}
		}
	}

	for _, s := range f.Stages {
		if s.Plan == "" && !s.HasAgent(AgentPlanning) && !s.Interactive && !s.IsAuto() && !s.IsScript() {
			return fmt.Errorf("stage %q: must have planning agent, a plan path, or script", s.ID)
		}
	}

	for _, s := range f.Stages {
		hasAuto := false
		for _, a := range s.Agents {
			if a == AgentAuto {
				hasAuto = true
				break
			}
		}
		if !hasAuto {
			continue
		}
		if len(s.Agents) != 1 {
			return fmt.Errorf("stage %q: \"auto\" must be the only agent", s.ID)
		}
	}

	for _, s := range f.Stages {
		if !s.IsScript() {
			continue
		}
		if len(s.Agents) > 0 {
			return fmt.Errorf("stage %q: \"script\" cannot be combined with agents", s.ID)
		}
		if s.Command != "" {
			return fmt.Errorf("stage %q: \"script\" cannot be combined with command", s.ID)
		}
		if s.Interactive {
			return fmt.Errorf("stage %q: \"script\" cannot be combined with interactive", s.ID)
		}
		if s.Plan != "" {
			return fmt.Errorf("stage %q: \"script\" cannot be combined with plan", s.ID)
		}
		if s.Verify != "" {
			return fmt.Errorf("stage %q: \"script\" cannot be combined with verify", s.ID)
		}
	}

	for _, s := range f.Stages {
		if len(s.Buttons) == 0 {
			continue
		}
		if s.IsScript() {
			return fmt.Errorf("stage %q: \"buttons\" cannot be combined with script", s.ID)
		}
		seen := make(map[string]bool, len(s.Buttons))
		for _, btn := range s.Buttons {
			if btn.Label == "" {
				return fmt.Errorf("stage %q: button label cannot be empty", s.ID)
			}
			if btn.Prompt == "" {
				return fmt.Errorf("stage %q: button %q has empty prompt", s.ID, btn.Label)
			}
			if seen[btn.Label] {
				return fmt.Errorf("stage %q: duplicate button label %q", s.ID, btn.Label)
			}
			seen[btn.Label] = true
		}
	}

	if err := detectCycles(f.Stages); err != nil {
		return err
	}

	// Build artifact index: stageID -> artifactName -> true
	artifactIndex := make(map[string]map[string]bool, len(f.Stages))
	for _, s := range f.Stages {
		names := make(map[string]bool, len(s.Artifacts))
		for _, a := range s.Artifacts {
			if names[a.Name] {
				return fmt.Errorf("stage %q: duplicate artifact name %q", s.ID, a.Name)
			}
			names[a.Name] = true
		}
		artifactIndex[s.ID] = names
	}

	// Validate inputs
	for _, s := range f.Stages {
		depsSet := make(map[string]bool, len(s.DependsOn))
		for _, d := range s.DependsOn {
			depsSet[d] = true
		}

		for _, inp := range s.Inputs {
			parts := strings.SplitN(inp.Ref, ".", 2)
			if len(parts) != 2 {
				return fmt.Errorf("stage %q: invalid input ref %q (expected stage.artifact)", s.ID, inp.Ref)
			}
			stageID, artName := parts[0], parts[1]

			if !ids[stageID] {
				return fmt.Errorf("stage %q: input ref %q references unknown stage %q", s.ID, inp.Ref, stageID)
			}
			if !depsSet[stageID] {
				return fmt.Errorf("stage %q: input ref %q references stage %q which is not in depends_on", s.ID, inp.Ref, stageID)
			}
			arts, ok := artifactIndex[stageID]
			if !ok || !arts[artName] {
				return fmt.Errorf("stage %q: input ref %q references unknown artifact %q in stage %q", s.ID, inp.Ref, artName, stageID)
			}
		}
	}

	// Validate Reflect v3 configuration
	for _, s := range f.Stages {
		if s.Reflect == nil {
			continue
		}
		// Reflect requires memory.path to be set
		if !f.MemoryEnabled() {
			return fmt.Errorf("stage %q: reflect requires memory.path", s.ID)
		}
		// reflect.file is required
		if s.Reflect.File == "" {
			return fmt.Errorf("stage %q: reflect.file is required", s.ID)
		}
		// reflect.mode must be one of r, w, rw
		if s.Reflect.Mode != "" && s.Reflect.Mode != ReflectModeR && s.Reflect.Mode != ReflectModeW && s.Reflect.Mode != ReflectModeRW {
			return fmt.Errorf("stage %q: reflect.mode must be r, w, or rw", s.ID)
		}
	}

	return nil
}

func detectCycles(stages []Stage) error {
	deps := make(map[string][]string, len(stages))
	for _, s := range stages {
		deps[s.ID] = s.DependsOn
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(stages))

	var visit func(id string) error
	visit = func(id string) error {
		if color[id] == black {
			return nil
		}
		if color[id] == gray {
			return fmt.Errorf("cycle detected involving stage %q", id)
		}
		color[id] = gray
		for _, dep := range deps[id] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		color[id] = black
		return nil
	}

	for _, s := range stages {
		if err := visit(s.ID); err != nil {
			return err
		}
	}
	return nil
}
