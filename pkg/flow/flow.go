package flow

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/skills"
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

// Reflect — конфиг отражения стадии для agent-памяти v3. Mode — r|w|rw, режим доступа агентов к памяти (0
// → по умолчанию rw).
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

// Button — предопределённая кнопка кебаб-меню стадии: Label — и отображаемая подпись, и ключ поиска;
// Prompt — инструкция, которая доставляется живому агенту через Revise при клике.
type Button struct {
	Label  string
	Prompt string
}

// Buttons сохраняет порядок объявления кнопок из YAML (плоский map[string]string итерировался бы случайно,
// тасуя меню). Декодируется из mapping-узла обходом value.Content попарно.
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
	// Skills are the skills the agent must load before work (a name or a list in YAML, exact repeats dropped).
	Skills skills.Names `yaml:"skills,omitempty,flow"`
	// SkillsDir is this stage's extra skill library; it wins over the flow's and the config's.
	SkillsDir skills.Dir `yaml:"skills_dir,omitempty"`
	DependsOn []string   `yaml:"depends_on,omitempty,flow"`
	// EagerPlanning starts the planning agent immediately without waiting for depends_on stages to finish
	// (the default before the planning-by-deps gate was introduced).
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
	// Verify описывает один или несколько шагов проверки завершённости стадии, выполняемых в проектной
	// директории после того как стадия заявила о завершении. См. pkg/flow/verify.go.
	Verify VerifySpec `yaml:"verify,omitempty"`
	// Prompt is an optional explicit instruction delivered to the agent
	// after the <stage> context block.
	Prompt string `yaml:"prompt,omitempty"`
	// Script, if set, makes this a script-only stage: it runs the given shell script instead of any agent,
	// with no planning/approval gate.
	Script        string        `yaml:"script,omitempty"`
	ScriptTimeout time.Duration `yaml:"script_timeout,omitempty"`
	// ScriptBefore/ScriptAfter run a shell script immediately before/after this stage's own main content
	// (agent, script, or interactive). Legal on any stage type, alongside its other fields.
	ScriptBefore        string        `yaml:"script_before,omitempty"`
	ScriptBeforeTimeout time.Duration `yaml:"script_before_timeout,omitempty"`
	ScriptAfter         string        `yaml:"script_after,omitempty"`
	ScriptAfterTimeout  time.Duration `yaml:"script_after_timeout,omitempty"`
	// AutoApprove approves a ready plan regardless of dashboard or --require-approval; default false.
	AutoApprove bool `yaml:"auto_approve,omitempty"`
	// AutoRun, если явно false, приостанавливает стадию сразу при первой активации вместо немедленного старта
	// — стадия уходит в paused с PausedFrom=pending и ждёт Continue.
	AutoRun *bool `yaml:"auto_run,omitempty"`
	// Buttons — предопределённые пункты кебаб-меню этой стадии: label → prompt. Клик доставляет prompt живому
	// агенту через Revise (тот же путь, что и свободная заметка).
	Buttons Buttons `yaml:"buttons,omitempty"`
	// Reflect: конфиг отражения этой стадии для agent-памяти. nil (не задано) — reflect отключен.
	Reflect *Reflect `yaml:"reflect,omitempty"`
	// MemoryUse: переопределяет глобальный memory.memory_use для ЭТОЙ стадии. nil — наследует глобальное
	// значение; true/false — задаёт явно.
	MemoryUse *bool `yaml:"memory_use,omitempty"`
	// Hooks — lifecycle-хуки, ограниченные этой стадией (flow-события в них
	// запрещены валидацией).
	Hooks []lifecyclehooks.Hook `yaml:"hooks,omitempty"`
	// Browser (managed Chrome DevTools capability): explicit false opts this
	// stage out of the afm_chrome MCP. nil — inherits the global browser policy.
	Browser *bool `yaml:"browser,omitempty"`
	// Assistants — helper profiles auto-attached at the start of this stage's execution phase, the same way
	// a manual dashboard Attach click would. See pkg/flow/assistants.go.
	Assistants StageAssistants `yaml:"assistants,omitempty"`
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

// IsExecutionStage reports whether the stage ever runs an implementation/review/autonomous pass — as
// opposed to a planning-only stage, which never "declares completion" (no .done, no execution_summary.md).
// Shared by the verify and assistants validate() rules: both features attach to that completion moment, so
// neither can do anything on a stage that never reaches it.
func (s *Stage) IsExecutionStage() bool {
	return s.HasAgent(AgentImplementation) || s.HasAgent(AgentReview) || s.IsAuto()
}

// AutoRunDisabled reports whether this stage's first activation should pause
// instead of starting immediately (auto_run explicitly set to false).
func (s Stage) AutoRunDisabled() bool {
	return s.AutoRun != nil && !*s.AutoRun
}

// BrowserDisabled reports whether this stage explicitly opts out of the managed Chrome DevTools browser
// capability (browser: false). nil inherits the global policy.
func (s Stage) BrowserDisabled() bool {
	return s.Browser != nil && !*s.Browser
}

// MemoryConfig — настройки agent-памяти флоу (v3 — директория).
// Path непустой включает всю фичу (см. docs/superpowers/specs/2026-08-28-agent-memory-v3-design.md).
type MemoryConfig struct {
	// Path — директория для хранения памяти (project-wide <path>/memory.md +
	// per-stage файлы из Reflect.File), относительно root_dir. Непусто = фича включена.
	Path string `yaml:"path,omitempty"`
	// Mode — режим ГЛОБАЛЬНОГО memory.md: r, w, rw. "" → дефолт "rw" (ставится в ParseFile).
	Mode string `yaml:"mode,omitempty"`
	// MemoryUse — глобальный выключатель УЧАСТИЯ стадий в чтении памяти. Управляет ТОЛЬКО чтением
	// (инъекцией); запись per-stage файлов управляется Reflect, запись memory.md — полем Mode выше.
	MemoryUse bool `yaml:"memory_use,omitempty"`
	// MaxRules — максимальное количество паттернов на файл (project и per-stage).
	// 0 → дефолт 25 (ставится в ParseFile).
	MaxRules int `yaml:"max_rules,omitempty"`
	// Commit — коммитить ли изменения памяти в git (опционально).
	Commit bool `yaml:"commit,omitempty"`
}

// CanReadProject reports whether the project-wide memory.md may be injected
// into stage prompts (Mode is "r" or "rw").
func (c MemoryConfig) CanReadProject() bool {
	return c.Mode == ReflectModeR || c.Mode == ReflectModeRW
}

// CanWriteProject reports whether the end-of-run pass may write/update the
// project-wide memory.md (Mode is "w" or "rw").
func (c MemoryConfig) CanWriteProject() bool {
	return c.Mode == ReflectModeW || c.Mode == ReflectModeRW
}

// UseFor resolves the effective per-stage memory participation: the stage's own MemoryUse override if set,
// otherwise the global MemoryUse.
func (c MemoryConfig) UseFor(stageMemoryUse *bool) bool {
	if stageMemoryUse != nil {
		return *stageMemoryUse
	}
	return c.MemoryUse
}

// Flow is the top-level structure parsed from a flow YAML file.
type Flow struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// Prompt is shared text added to the system prompt of every stage and every phase
	// (planning/implementation/review). Empty value does not change behavior.
	Prompt      string `yaml:"prompt,omitempty"`
	MaxParallel int    `yaml:"max_parallel,omitempty"`
	// RootDir — корень проекта, в котором выполняются агенты. Без него агенты наследуют CWD afm и резолвят
	// относительные пути проекта (docs/arch и т.п.) в чужом корне.
	RootDir string       `yaml:"root_dir,omitempty"`
	Memory  MemoryConfig `yaml:"memory,omitempty"`
	// SkillsDir is the flow's extra skill library, used by stages and team members that set none of their own.
	SkillsDir skills.Dir `yaml:"skills_dir,omitempty"`
	// Hooks — lifecycle-хуки флоу-слоя: получают события всех стадий.
	Hooks []lifecyclehooks.Hook `yaml:"hooks,omitempty"`
	// SideAgent, when set, overrides config/env for the dashboard side-agent
	// (nil = no opinion → defer to AFM_SIDEAGENT / config). Highest precedence.
	SideAgent *bool `yaml:"side_agent,omitempty"`
	// Worktrees, when set, overrides env/config for per-flow git worktrees
	// (nil = no opinion → defer to AFM_WORKTREES / config). Highest precedence.
	Worktrees *bool `yaml:"worktrees,omitempty"`
	// Collaboration overrides the helper-agent delegation depth for this flow only. Deliberately carries
	// NO per-flow "enabled": the feature is a deployment-wide switch (config.yaml's collaboration.enabled /
	// AFM_COLLABORATION), not something an individual flow can turn on for itself — see
	// FlowCollaboration.UnmarshalYAML.
	Collaboration *FlowCollaboration `yaml:"collaboration,omitempty"`
	// Team declares run agents created before the stages start (see TeamMember). Needs collaboration.
	Team Team `yaml:"team,omitempty"`
	// Repeat restarts the flow in a new run after every successful pass (see RepeatConfig).
	Repeat RepeatConfig `yaml:"repeat,omitempty"`
	Stages []Stage      `yaml:"stages"`
}

// defaultRepeatKeepRuns is how many successful runs a repeating flow keeps when keep_runs is not set.
const defaultRepeatKeepRuns = 2

// RepeatConfig is the flow's "repeat" setting. A missing key and "repeat: false" both leave the zero value
// (Enabled false): the flow runs once. "repeat: true" and a mapping enable it with the defaults below.
type RepeatConfig struct {
	Enabled bool
	// MaxCycles is the number of successful passes after which the loop stops; 0 means no limit.
	MaxCycles int
	// KeepRuns is how many successful runs of this process stay on disk; 0 keeps them all.
	KeepRuns int
}

// UnmarshalYAML accepts a boolean or a mapping with max_cycles and keep_runs. keep_runs defaults to 2 only
// when repeat is enabled, so an explicit "keep_runs: 0" needs no pointer. Mirrors FlowCollaboration's
// explicit-key parsing because yaml.v3 would silently ignore unknown keys. A null value never reaches this
// method — see rejectNullKey.
func (c *RepeatConfig) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		// yaml.v3 also decodes "yes", "on" and "Y" (even quoted) into a bool; the schema only allows true/false.
		var enabled bool
		if value.ShortTag() != "!!bool" || value.Decode(&enabled) != nil {
			return errRepeatInvalid
		}
		if enabled {
			*c = RepeatConfig{Enabled: true, KeepRuns: defaultRepeatKeepRuns}
		}
		return nil
	case yaml.MappingNode:
		*c = RepeatConfig{Enabled: true, KeepRuns: defaultRepeatKeepRuns}
		return c.unmarshalMapping(value)
	default:
		return errRepeatInvalid
	}
}

func (c *RepeatConfig) unmarshalMapping(value *yaml.Node) error {
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		var dst *int
		switch key {
		case "max_cycles":
			dst = &c.MaxCycles
		case "keep_runs":
			dst = &c.KeepRuns
		default:
			return fmt.Errorf("repeat: unknown field %q", key)
		}
		n, err := decodeInteger(value.Content[i+1])
		if err != nil {
			return fmt.Errorf("repeat.%s: %w", key, err)
		}
		*dst = n
	}
	return nil
}

// maxExactFloatInt is the largest magnitude below which every integer is exactly representable as a float64.
const maxExactFloatInt = 1 << 53

// decodeInteger reads an integer the way the JSON Schema "integer" type does: 3, 3.0 and 3e0 are accepted,
// 1.5 is not. yaml.v3 would silently truncate 1.5 to 1 when decoding into an int, so the tag is checked.
func decodeInteger(node *yaml.Node) (int, error) {
	switch node.ShortTag() {
	case "!!int":
		var n int
		if err := node.Decode(&n); err != nil {
			return 0, err
		}
		return n, nil
	case "!!float":
		var f float64
		if err := node.Decode(&f); err == nil && f == math.Trunc(f) && math.Abs(f) <= maxExactFloatInt {
			return int(f), nil
		}
	default:
	}
	return 0, fmt.Errorf("expected an integer, got %q", node.Value)
}

func (c RepeatConfig) validate() error {
	if c.MaxCycles < 0 {
		return fmt.Errorf("repeat.max_cycles: must not be negative, got %d", c.MaxCycles)
	}
	if c.KeepRuns < 0 {
		return fmt.Errorf("repeat.keep_runs: must not be negative, got %d", c.KeepRuns)
	}
	return nil
}

var errRepeatInvalid = errors.New("repeat must be true, false, or an object with max_cycles and keep_runs")

// FlowCollaboration is the per-flow override of collaboration.max_depth and collaboration.mode only.
type FlowCollaboration struct {
	MaxDepth *int
	// Mode is nil when the flow does not set collaboration.mode (defer to config).
	Mode *assistants.CollaborationMode
}

// UnmarshalYAML accepts only the max_depth and mode keys, rejecting "enabled" (and anything else) with a message
// pointing at where to actually enable the feature — mirrors verify.go's explicit-key-switch pattern
// (verifyStepFromMapping) rather than relying on yaml.v3's lenient default (which would otherwise silently
// ignore collaboration.enabled in a flow.yaml instead of rejecting it).
func (c *FlowCollaboration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return errors.New("collaboration: expected an object")
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		val := value.Content[i+1]
		switch key {
		case "max_depth":
			var v int
			if err := val.Decode(&v); err != nil {
				return fmt.Errorf("collaboration.max_depth: %w", err)
			}
			if v < 0 {
				return fmt.Errorf("collaboration.max_depth: must not be negative, got %d", v)
			}
			c.MaxDepth = &v
		case keyMode:
			mode, err := assistants.ParseCollaborationModeNode(val)
			if err != nil {
				return err
			}
			c.Mode = &mode
		case "enabled":
			return errors.New(`collaboration: unknown field "enabled" — there is no per-flow enable; turn the feature on/off via config.yaml's collaboration.enabled or AFM_COLLABORATION`)
		default:
			return fmt.Errorf("collaboration: unknown field %q", key)
		}
	}
	return nil
}

// MemoryEnabled сообщает, включена ли agent-память для этого флоу.
func (f *Flow) MemoryEnabled() bool { return f.Memory.Path != "" }

// ParseFile reads and validates a flow YAML file.
func ParseFile(path string) (*Flow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read flow file: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	var f Flow
	if len(root.Content) > 0 {
		if err := root.Decode(&f); err != nil {
			return nil, fmt.Errorf("parse yaml: %w", err)
		}
		if err := rejectNullKey(root.Content[0], "team", errTeamNotList); err != nil {
			return nil, err
		}
		if err := rejectNullKey(root.Content[0], "repeat", errRepeatInvalid); err != nil {
			return nil, err
		}
		if err := rejectNullSkillKeys(root.Content[0]); err != nil {
			return nil, err
		}
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	f.applyScriptTimeoutDefaults()
	if f.MemoryEnabled() {
		if f.Memory.MaxRules == 0 {
			f.Memory.MaxRules = 25
		}
		if f.Memory.Mode == "" {
			f.Memory.Mode = ReflectModeRW
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
	warnMemorySettingsWithoutPath(data, path)
	return &f, nil
}

// rejectNullSkillKeys rejects a bare "skills:" / "skills_dir:" at the flow and stage level, where yaml.v3
// would otherwise treat it as an absent key (team members check their own nodes).
func rejectNullSkillKeys(doc *yaml.Node) error {
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	if err := skills.RejectNullKeys(doc, keySkillsDir); err != nil {
		return err
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value != "stages" || doc.Content[i+1].Kind != yaml.SequenceNode {
			continue
		}
		for _, stage := range doc.Content[i+1].Content {
			if err := skills.RejectNullKeys(stage, keySkills, keySkillsDir); err != nil {
				return fmt.Errorf("stage: %w", err)
			}
		}
	}
	return nil
}

// memorySettingsProbe holds the flow-level memory.* keys as pointers so
// warnMemorySettingsWithoutPath can tell "key present" from "zero value".
type memorySettingsProbe struct {
	Path      *string `yaml:"path"`
	Mode      *string `yaml:"mode"`
	MemoryUse *bool   `yaml:"memory_use"`
	MaxRules  *int    `yaml:"max_rules"`
	Commit    *bool   `yaml:"commit"`
}

// warnMemorySettingsWithoutPath best-effort re-parses the raw YAML and warns if any memory.* setting is
// present while memory.path is empty.
func warnMemorySettingsWithoutPath(data []byte, path string) {
	var probe struct {
		Memory *memorySettingsProbe `yaml:"memory"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return
	}
	m := probe.Memory
	if m == nil || (m.Path != nil && *m.Path != "") {
		return
	}
	var set []string
	if m.Mode != nil {
		set = append(set, "mode")
	}
	if m.MemoryUse != nil {
		set = append(set, "memory_use")
	}
	if m.MaxRules != nil {
		set = append(set, "max_rules")
	}
	if m.Commit != nil {
		set = append(set, "commit")
	}
	if len(set) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "WARN: %s: memory.%s set without memory.path — agent memory is disabled and these settings are ignored (set memory.path to enable)\n", path, strings.Join(set, ", memory."))
}

// deprecatedSupervisorStageProbe holds just the removed per-stage
// LLM-supervisor keys, decoded by warnDeprecatedSupervisorFields.
type deprecatedSupervisorStageProbe struct {
	ID               string  `yaml:"id"`
	Supervisor       *bool   `yaml:"supervisor"`
	SupervisorPrompt *string `yaml:"supervisor_prompt"`
}

// warnDeprecatedSupervisorFields best-effort re-parses the raw YAML looking for the removed LLM-supervisor
// keys and prints a non-fatal WARN to stderr naming the affected stage id and key.
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

// defaultScriptTimeout bounds scripts and lifecycle before/after scripts to 300s when no timeout is
// configured.
const defaultScriptTimeout = 5 * time.Minute

// applyScriptTimeoutDefaults fills in defaultScriptTimeout for any script field that is set but whose
// timeout was left at the Go zero value.
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

// StageIDSafe сообщает, безопасен ли s как stage.ID — единственный charset-валидатор, переиспользуемый
// pkg/orchestrator/graph (Этап 9, T9.1) при проверке id динамически добавляемой стадии вместо второго,
// независимого regex. Кроме пути на диске (<runDir>/<stageID>) id попадает в URL managed-browser сессии
// (/session/<epoch>/<stageID>) и в имя профиля Chrome.
func StageIDSafe(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func (f *Flow) validate() error {
	ids := make(map[string]bool, len(f.Stages))
	// Reject case-folded ID collisions: case-insensitive filesystems would share Chrome profiles;
	// dependencies remain case-sensitive.
	folded := make(map[string]string, len(f.Stages))
	for _, s := range f.Stages {
		if ids[s.ID] {
			return fmt.Errorf("duplicate stage id: %q", s.ID)
		}
		ids[s.ID] = true
		fold := strings.ToLower(s.ID)
		if prev, ok := folded[fold]; ok {
			return fmt.Errorf("stage ids %q and %q collide case-insensitively (they map to the same session/profile on a case-insensitive filesystem)", prev, s.ID)
		}
		folded[fold] = s.ID
	}

	if err := f.Team.validate(); err != nil {
		return err
	}

	if err := f.Repeat.validate(); err != nil {
		return err
	}

	// stage.ID входит в пути и URL; допускаем только [A-Za-z0-9._-], исключая пустую строку, "." и "..".
	for _, s := range f.Stages {
		if !StageIDSafe(s.ID) {
			return fmt.Errorf("stage %q: id must match [A-Za-z0-9._-] (non-empty, not \".\"/\"..\")", s.ID)
		}
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
		if !s.Verify.IsEmpty() {
			return fmt.Errorf("stage %q: \"script\" cannot be combined with verify", s.ID)
		}
	}

	// verify: сами шаги и требование исполняемой стадии. Стадия без implementation/review/auto (только
	// planning) не создаёт .done и никогда не "заявляет о завершении" — проверять там нечего.
	for _, s := range f.Stages {
		if s.Verify.IsEmpty() {
			continue
		}
		if err := s.Verify.validate(s.ID); err != nil {
			return err
		}
		if !s.IsExecutionStage() {
			return fmt.Errorf("stage %q: verify requires an execution stage (planning-only stage cannot have verify)", s.ID)
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

	for _, s := range f.Stages {
		if len(s.Assistants) == 0 {
			continue
		}
		if s.IsScript() {
			return fmt.Errorf("stage %q: \"assistants\" cannot be combined with script", s.ID)
		}
		// Mirrors the verify check above: attachDeclaredAssistants gates on kind != kindPlanning, so a
		// planning-only stage's assistants: would pass validation but never actually attach, on any run.
		if !s.IsExecutionStage() {
			return fmt.Errorf("stage %q: assistants requires an execution stage (planning-only stage would never attach them)", s.ID)
		}
		seen := make(map[string]bool, len(s.Assistants))
		for i, a := range s.Assistants {
			if a.Profile == "" {
				return fmt.Errorf("stage %q: assistants[%d]: profile is required", s.ID, i)
			}
			if seen[a.Profile] {
				return fmt.Errorf("stage %q: duplicate assistant profile %q", s.ID, a.Profile)
			}
			seen[a.Profile] = true
			if !a.Mode.Valid() {
				return fmt.Errorf("stage %q: assistants[%d]: mode must be %q or %q, got %q", s.ID, i, assistants.ModeAdvisory, assistants.ModeRequired, a.Mode)
			}
			if !a.Trigger.Valid() {
				return fmt.Errorf("stage %q: assistants[%d]: trigger must be %q, %q, or %q, got %q", s.ID, i, assistants.TriggerOneShot, assistants.TriggerCheckpoints, assistants.TriggerOnCompletion, a.Trigger)
			}
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

	// memory.mode (global memory.md lifecycle) must be r/w/rw if set.
	if f.Memory.Mode != "" && f.Memory.Mode != ReflectModeR && f.Memory.Mode != ReflectModeW && f.Memory.Mode != ReflectModeRW {
		return errors.New("memory.mode must be r, w, or rw")
	}

	// memory.max_rules must be non-negative.
	if f.Memory.MaxRules < 0 {
		return errors.New("memory.max_rules must be >= 0")
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
		// reflect.file must be a local relative path — no ".." escaping the memory directory and no absolute
		// path (it's joined onto memory.path, see memory.StageFile).
		if !filepath.IsLocal(s.Reflect.File) {
			return fmt.Errorf("stage %q: reflect.file %q must be a local relative path (no .. or absolute)", s.ID, s.Reflect.File)
		}
		// reflect.file must not collide (after Clean, catching the "./" alias)
		// with the fixed project memory file name.
		if filepath.Clean(s.Reflect.File) == "memory.md" {
			return fmt.Errorf("stage %q: reflect.file must not be memory.md (collides with the project memory file)", s.ID)
		}
		// reflect.mode must be one of r, w, rw
		if s.Reflect.Mode != "" && s.Reflect.Mode != ReflectModeR && s.Reflect.Mode != ReflectModeW && s.Reflect.Mode != ReflectModeRW {
			return fmt.Errorf("stage %q: reflect.mode must be r, w, or rw", s.ID)
		}
	}

	if err := lifecyclehooks.ValidateLayer(f.Hooks, false); err != nil {
		return fmt.Errorf("hooks: %w", err)
	}
	stageHookIDs := map[string]string{} // hook id -> stage id (первый объявивший)
	for _, s := range f.Stages {
		if err := lifecyclehooks.ValidateLayer(s.Hooks, true); err != nil {
			return fmt.Errorf("stage %q: %w", s.ID, err)
		}
		for _, h := range s.Hooks {
			if other, dup := stageHookIDs[h.ID]; dup {
				return fmt.Errorf("stage %q: hooks: duplicate hook id %q across stages (already used by stage %q)", s.ID, h.ID, other)
			}
			stageHookIDs[h.ID] = s.ID
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
