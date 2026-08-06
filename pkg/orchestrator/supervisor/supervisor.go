package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"text/template"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
)

// MaxRetries — число повторных попыток после первого запуска (всего MaxRetries+1).
// Сверху ограничено idle_timeout stage (30м default): каждая попытка ≈ agent-runtime,
// так что реально успевает меньше — idle_timeout добьёт лишнее.
// Может переопределяться в тестах ДО создания Supervisor.
var MaxRetries = 15

// RetryBackoff — фиксированная пауза между попытками после retryable-ошибки
// (529/502/503/504, rate limit).
// Может переопределяться в тестах ДО создания Supervisor.
var RetryBackoff = 5 * time.Second

// phaseAutonomous — строковая константа для автономной фазы выполнения.
const phaseAutonomous = string(flow.PhaseAutonomous)

// isRetryableError проверяет, является ли ошибка rate limit или server error (повторяемой с backoff).
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	patterns := []string{
		"hit your limit",
		"rate limit",
		"too many requests",
		"overloaded",
		"at capacity",
		"http 500",
		"status 500",
		"internal server error",
		"api error: 529",
		"api error: 502",
		"api error: 503",
		"api error: 504",
	}
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// EvaluationResult — ответ супервизора на оценку стадии.
type EvaluationResult struct {
	CanExecuteAutonomously bool     `json:"can_execute_autonomously"`
	Reason                 string   `json:"reason"`
	RecommendedPhases      []string `json:"recommended_phases"`
}

// Supervisor оценивает стадию и решает, можно ли выполнить её автономно.
type Supervisor struct {
	runner executor.Runner
	// maxRetries/retryBackoff — снапшоты globals (см. Orchestrator): EvaluateStage
	// ретраит transient-ошибки и может выполняться в горутине, переживая Run().
	maxRetries   int
	retryBackoff time.Duration
}

// NewSupervisor создаёт Supervisor с заданным runner.
func NewSupervisor(r executor.Runner) *Supervisor {
	return &Supervisor{
		runner:       r,
		maxRetries:   MaxRetries,
		retryBackoff: RetryBackoff,
	}
}

// supervisorPromptTmpl — системный промпт на английском (экономия токенов).
const supervisorPromptTmpl = `You are an AI Supervisor in the afm CLI orchestrator. Determine if a stage can execute autonomously in a single step using its attached skills, or requires the standard multi-phase development cycle.

GLOBAL PROJECT RULES:
<global_prompt>
{{.GlobalPrompt}}
</global_prompt>

CURRENT STAGE:
- ID: {{.Stage.ID}}
- Description: {{.Stage.Description}}
- Attached Skills: {{.SkillsList}}
- Base Phases (configured by user): {{.BasePhases}}
{{if .Stage.SupervisorPrompt}}
EXTRA STAGE-SPECIFIC INSTRUCTIONS (Highest priority):
<local_supervisor_prompt>
{{.Stage.SupervisorPrompt}}
</local_supervisor_prompt>
{{end}}
CONSTRAINTS:
1. "recommended_phases" MUST be exactly one of:
   - ["autonomous_execution"] — skill handles the entire task end-to-end without planning.
   - {{.BasePhases}} — standard cycle required (unsafe, unclear, or needs human approval).
2. NEVER add phases not present in Base Phases.

Respond with ONLY this JSON (no markdown, no preamble):
{
  "can_execute_autonomously": <true|false>,
  "reason": "<concise justification referencing skills and local prompt>",
  "recommended_phases": [<"autonomous_execution" or base phases list>]
}`

type supervisorTmplData struct {
	GlobalPrompt string
	Stage        flow.Stage
	SkillsList   string
	// BasePhases — JSON-массив фаз стадии (напр. `["planning","implementation"]`).
	// Рендерим как JSON, а не через Go-печать слайса (`[planning implementation]`),
	// иначе LLM иногда копирует base phases одной строкой "planning implementation".
	BasePhases string
}

var supervisorTmpl = template.Must(template.New("supervisor").Parse(supervisorPromptTmpl))

func compileSupervisorPrompt(stage flow.Stage, globalPrompt string) (string, error) {
	bp, err := json.Marshal(AgentTypesToStrings(stage.Agents))
	if err != nil {
		return "", fmt.Errorf("marshal base phases: %w", err)
	}
	data := supervisorTmplData{
		GlobalPrompt: globalPrompt,
		Stage:        stage,
		SkillsList:   formatSkills(stage.Skills),
		BasePhases:   string(bp),
	}
	var buf bytes.Buffer
	if err := supervisorTmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("compile supervisor prompt: %w", err)
	}
	return buf.String(), nil
}

// AgentTypesToStrings конвертирует []flow.AgentType в []string.
// Определена здесь (Task 3), переиспользуется в Task 7 (DetermineStagePhases).
func AgentTypesToStrings(agents []flow.AgentType) []string {
	ss := make([]string, len(agents))
	for i, a := range agents {
		ss[i] = string(a)
	}
	return ss
}

func formatSkills(skills []string) string {
	if len(skills) == 0 {
		return "(none)"
	}
	parts := make([]string, len(skills))
	copy(parts, skills)
	return strings.Join(parts, ", ")
}

// EvaluateStage вызывает LLM и возвращает решение о треке стадии.
// При любой ошибке вызывающий должен использовать базовые фазы (фолбэк).
func (s *Supervisor) EvaluateStage(ctx context.Context, stage flow.Stage, globalPrompt string) (*EvaluationResult, error) {
	prompt, err := compileSupervisorPrompt(stage, globalPrompt)
	if err != nil {
		return nil, err
	}
	// Supervisor идёт через те же ретраи на transient-ошибки (529/502/503/504),
	// что и stage-агенты (retry.go): z.ai overload переживается ретраем с backoff,
	// а не немедленным фолбэком на базовые фазы. На non-retryable — сразу ошибка.
	var raw []byte
	for attempt := 0; ; attempt++ {
		raw, err = s.runner.RunJSONQuery(ctx, prompt)
		if err == nil {
			break
		}
		if !isRetryableError(err) {
			return nil, fmt.Errorf("supervisor LLM call: %w", err)
		}
		if attempt >= s.maxRetries {
			return nil, fmt.Errorf("supervisor LLM call: retries exhausted: %w", err)
		}
		log.Printf("supervisor: stage %s transient error (attempt %d/%d, retry in %v): %v",
			stage.ID, attempt+1, s.maxRetries, s.retryBackoff, err)
		select {
		case <-time.After(s.retryBackoff):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	result, err := parseDecision(raw)
	if err != nil {
		return nil, err
	}
	if err := validateDecision(&result, stage); err != nil {
		return nil, err
	}
	return &result, nil
}

// parseDecision извлекает EvaluationResult из сырого stdout команды LLM.
// Обрабатывает три формы вывода:
//  1. Сырой decision JSON (кастомная JSON-команда) — прямой Unmarshal.
//  2. Конверт claude: {"type":"result","result":"<decision JSON или fenced>",...}
//     — извлекаем поле result, снимаем фенсы/произвольный текст, парсим внутренний JSON.
//  3. Иначе — ошибка (ни одна форма не распознана).
func parseDecision(raw []byte) (EvaluationResult, error) {
	trimmed := bytes.TrimSpace(raw)

	// (1) Сырой decision JSON напрямую.
	var direct EvaluationResult
	if err := json.Unmarshal(trimmed, &direct); err == nil && hasDecisionContent(direct) {
		return direct, nil
	}

	// (2) Конверт claude: {"type":"result","result":"<decision JSON>", ...}.
	var envelope struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(trimmed, &envelope); err == nil && envelope.Result != "" {
		var inner EvaluationResult
		if err := json.Unmarshal([]byte(extractJSON(envelope.Result)), &inner); err == nil {
			return inner, nil
		}
	}

	// (3) claude --output-format json в актуальных версиях = массив событий
	// [{"type":"system",...}, ..., {"type":"result","result":"<decision JSON>"}].
	// Берём последний element с type=result и парсим его result.
	var arr []struct {
		Type   string `json:"type"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal(trimmed, &arr); err == nil {
		for i := len(arr) - 1; i >= 0; i-- {
			if arr[i].Type == "result" && arr[i].Result != "" {
				var inner EvaluationResult
				if err := json.Unmarshal([]byte(extractJSON(arr[i].Result)), &inner); err == nil {
					return inner, nil
				}
			}
		}
	}

	return EvaluationResult{}, fmt.Errorf("parse supervisor response: cannot extract decision JSON (raw: %.200s)", raw)
}

// hasDecisionContent отличает реальный decision от нулевых полей (например,
// если конверт случайно распарсился напрямую в EvaluationResult без значимых данных).
func hasDecisionContent(r EvaluationResult) bool {
	return len(r.RecommendedPhases) > 0 || r.CanExecuteAutonomously || r.Reason != ""
}

// extractJSON снимает markdown-фенсы (```json / ```) и обрезает строку до
// внешнего {...}, отбрасывая сопутствующий текст до/после JSON-объекта.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "```json"), "```")
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if start := strings.Index(s, "{"); start >= 0 {
		if end := strings.LastIndex(s, "}"); end > start {
			return s[start : end+1]
		}
	}
	return s
}

// validateDecision проверяет решение супервизора.
//
// Autonomous-решение — единственное, что влияет на поведение (запускает
// runAutonomousAgent), поэтому валидируем строго: ровно ["autonomous_execution"].
//
// Standard-решение: recommended_phases — advisory. DetermineStagePhases в любом
// случае возвращает base (агенты стадии), фазы супервизора не используются.
// Поэтому НЕ rejectаем standard за malformed phases — LLM иногда пишет
// "planning implementation" одной строкой (артефакт рендера слайса), и прежний
// строгий контроль давал ложный fallback, который прятал валидное решение из
// лога/UI. "Только сокращает фазы" гарантируется самим DetermineStagePhases
// (всегда base для standard, ["autonomous_execution"] для autonomous).
func validateDecision(result *EvaluationResult, stage flow.Stage) error {
	if result.CanExecuteAutonomously {
		if len(result.RecommendedPhases) != 1 || result.RecommendedPhases[0] != phaseAutonomous {
			return fmt.Errorf("supervisor: autonomous decision must recommend [\"autonomous_execution\"], got %v", result.RecommendedPhases)
		}
	}
	return nil
}
