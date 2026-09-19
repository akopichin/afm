package flow

import (
	"errors"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// VerifyKind различает два вида verify-шага: обычная shell-команда или
// делегирование агенту (напр. codex), выполняющему проверку сам.
type VerifyKind int

const (
	VerifyShell VerifyKind = iota
	VerifyAgent
)

// VerifyStep — один шаг проверки завершённости стадии.
type VerifyStep struct {
	Kind VerifyKind
	// Run — shell-команда (Kind == VerifyShell).
	Run string
	// Command — алиас агента-исполнителя (Kind == VerifyAgent), напр. "codex".
	Command string
	// Prompt — дополнительный критерий для агента (необязателен).
	Prompt string
	// Timeout — необязательный таймаут шага; 0 значит "не задан".
	Timeout time.Duration
}

// VerifySpec — нормализованный verify стадии: один или несколько шагов
// (shell и/или агент), выполняемых по порядку после того как стадия заявила
// о завершении. Заменяет прежний Stage.Verify string, сохраняя старое
// поведение при скалярной форме YAML.
type VerifySpec struct {
	Steps []VerifyStep
	// fromScalar отмечает, что спека получена из legacy-скаляра
	// (verify: "команда") — используется ТОЛЬКО для симметричной сериализации
	// обратно в тот же вид (см. MarshalYAML). На валидацию и исполнение не
	// влияет.
	fromScalar bool
}

// NewShellVerify собирает VerifySpec из одной shell-команды программно —
// то же самое, что получилось бы при разборе YAML-скаляра verify: "команда".
// Нужен там, где Stage.Verify собирается в памяти, а не через YAML (afm
// init, cmd/afm/init_stage.go) — fromScalar не экспортирован, так что
// прямое присвоение поля недоступно за пределами пакета flow.
func NewShellVerify(cmd string) VerifySpec {
	if cmd == "" {
		return VerifySpec{}
	}
	return VerifySpec{Steps: []VerifyStep{{Kind: VerifyShell, Run: cmd}}, fromScalar: true}
}

// IsEmpty сообщает, что verify не задан — стадия завершается без проверки.
func (s VerifySpec) IsEmpty() bool { return len(s.Steps) == 0 }

// IsZero нужен yaml.v3 для тега omitempty: пустая спека опускается при
// сериализации целиком, а не пишется как пустой список/объект.
func (s VerifySpec) IsZero() bool { return s.IsEmpty() }

// UnmarshalYAML разбирает три формы verify (см. docs/superpowers/sdd —
// задача V1 фичи ai-verify):
//   - скаляр — один shell-шаг, verify: "cmd" (в т.ч. verify: "codex" —
//     это ВСЕГДА shell, а не агент; агентский шаг возможен только через
//     объектную форму {command: ...});
//   - объект — один шаг, {run: ...} (shell) либо {command: ..., prompt: ...} (агент);
//   - список объектов — несколько шагов по порядку.
//
// Здесь отклоняются только структурные проблемы (неизвестные поля, вложенный
// список, некорректный формат timeout, пустой список). Бизнес-правила —
// взаимоисключение run/command, обязательность run/command, отрицательный
// timeout — проверяет VerifySpec.validate(stageID): там есть id стадии и
// 1-based индекс шага для точного текста ошибки, недоступные здесь.
func (s *VerifySpec) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Value == "" {
			*s = VerifySpec{}
			return nil
		}
		*s = VerifySpec{
			Steps:      []VerifyStep{{Kind: VerifyShell, Run: value.Value}},
			fromScalar: true,
		}
		return nil

	case yaml.MappingNode:
		step, err := verifyStepFromMapping(value)
		if err != nil {
			return err
		}
		*s = VerifySpec{Steps: []VerifyStep{step}}
		return nil

	case yaml.SequenceNode:
		if len(value.Content) == 0 {
			return errors.New("verify: step list must not be empty")
		}
		steps := make([]VerifyStep, 0, len(value.Content))
		for _, item := range value.Content {
			if item.Kind != yaml.MappingNode {
				return errors.New("verify: each list item must be an object ({run: ...} or {command: ...})")
			}
			step, err := verifyStepFromMapping(item)
			if err != nil {
				return err
			}
			steps = append(steps, step)
		}
		*s = VerifySpec{Steps: steps}
		return nil

	default:
		return errors.New("verify: expected a string, an object, or a list of objects")
	}
}

// verifyStepFromMapping декодирует один mapping-узел verify в VerifyStep.
func verifyStepFromMapping(node *yaml.Node) (VerifyStep, error) {
	var step VerifyStep
	var timeoutRaw string
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		switch key {
		case "run":
			step.Run = val.Value
		case "command":
			step.Command = val.Value
		case "prompt":
			step.Prompt = val.Value
		case "timeout":
			timeoutRaw = val.Value
		case "agent":
			return VerifyStep{}, errors.New(`verify: unknown field "agent"; use "command"`)
		default:
			return VerifyStep{}, fmt.Errorf("verify: unknown field %q", key)
		}
	}
	if timeoutRaw != "" {
		d, err := time.ParseDuration(timeoutRaw)
		if err != nil {
			return VerifyStep{}, fmt.Errorf("verify: invalid timeout %q: %w", timeoutRaw, err)
		}
		step.Timeout = d
	}
	if step.Command != "" {
		step.Kind = VerifyAgent
	}
	return step, nil
}

// MarshalYAML сериализует VerifySpec обратно в ту же форму, которую понимает
// UnmarshalYAML: одна shell-команда, полученная из legacy-скаляра, снова
// становится скаляром (fromScalar), один шаг — объектом, несколько шагов —
// списком объектов. Нужно для afm init, который собирает flow.Stage в памяти
// и затем сериализует его в flow.yaml.
func (s VerifySpec) MarshalYAML() (any, error) {
	if s.IsEmpty() {
		return nil, nil
	}
	if len(s.Steps) == 1 && s.fromScalar {
		return s.Steps[0].Run, nil
	}
	if len(s.Steps) == 1 {
		return verifyStepToMap(s.Steps[0]), nil
	}
	out := make([]map[string]any, len(s.Steps))
	for i, step := range s.Steps {
		out[i] = verifyStepToMap(step)
	}
	return out, nil
}

func verifyStepToMap(step VerifyStep) map[string]any {
	m := make(map[string]any, 3)
	if step.Kind == VerifyAgent {
		m["command"] = step.Command
		if step.Prompt != "" {
			m["prompt"] = step.Prompt
		}
	} else {
		m["run"] = step.Run
	}
	if step.Timeout > 0 {
		m["timeout"] = step.Timeout.String()
	}
	return m
}

// VerifyAgentCommands возвращает команды всех агентских шагов verify стадии
// (Kind == VerifyAgent), без дублей внутри стадии, в порядке появления.
// Shell-шаги пропускаются — у них нет отдельного агента-исполнителя.
// Нужен там, где команду агента нужно обнаружить ДАЖЕ если она встречается
// только в verify, а не в Stage.Command: монтирование Docker-бинарников,
// discovery recipe-ключей, командные семафоры Manager (см. пакет
// pkg/orchestrator/concurrency, задача V3 фичи ai-verify).
func (s Stage) VerifyAgentCommands() []string {
	if s.Verify.IsEmpty() {
		return nil
	}
	seen := make(map[string]bool)
	var cmds []string
	for _, step := range s.Verify.Steps {
		if step.Kind != VerifyAgent || step.Command == "" || seen[step.Command] {
			continue
		}
		seen[step.Command] = true
		cmds = append(cmds, step.Command)
	}
	return cmds
}

// validate проверяет бизнес-правила шагов verify: ровно один из run/command
// на шаг, непустое значение выбранного поля, неотрицательный timeout.
// Индексы в сообщениях — 1-based (как в остальных ошибках flow.go).
func (s VerifySpec) validate(stageID string) error {
	for i, step := range s.Steps {
		idx := i + 1
		switch {
		case step.Run != "" && step.Command != "":
			return fmt.Errorf("stage %q: verify[%d]: run and command are mutually exclusive", stageID, idx)
		case step.Run == "" && step.Command == "":
			return fmt.Errorf("stage %q: verify[%d]: must set run or command", stageID, idx)
		}
		if step.Timeout < 0 {
			return fmt.Errorf("stage %q: verify[%d]: timeout must not be negative", stageID, idx)
		}
	}
	return nil
}
