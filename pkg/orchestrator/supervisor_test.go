package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
)

// mockJSONRunner реализует executor.Runner с настраиваемым RunJSONQuery.
type mockJSONRunner struct {
	response []byte
	err      error
}

func (m *mockJSONRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return m.response, m.err
}
func (m *mockJSONRunner) RunPlanning(_ context.Context, _, _, _, _ string) error { return nil }
func (m *mockJSONRunner) RunAgent(_ context.Context, _, _, _, _ string) error    { return nil }

func makeTestStage(supervisor bool, agents []flow.AgentType, skills []string) flow.Stage {
	return flow.Stage{
		ID:          "test-stage",
		Description: "do the thing",
		Supervisor:  supervisor,
		Agents:      agents,
		Skills:      skills,
	}
}

func TestSupervisor_Autonomous(t *testing.T) {
	runner := &mockJSONRunner{
		response: []byte(`{"can_execute_autonomously":true,"reason":"skill handles it","recommended_phases":["autonomous_execution"]}`),
	}
	s := NewSupervisor(runner)
	stage := makeTestStage(true, []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, []string{"goga:apply"})

	result, err := s.EvaluateStage(context.Background(), stage, "global context")
	if err != nil {
		t.Fatal(err)
	}
	if !result.CanExecuteAutonomously {
		t.Error("expected autonomous=true")
	}
	if len(result.RecommendedPhases) != 1 || result.RecommendedPhases[0] != "autonomous_execution" {
		t.Errorf("unexpected phases: %v", result.RecommendedPhases)
	}
}

func TestSupervisor_Standard(t *testing.T) {
	runner := &mockJSONRunner{
		response: []byte(`{"can_execute_autonomously":false,"reason":"needs planning","recommended_phases":["planning","implementation"]}`),
	}
	s := NewSupervisor(runner)
	stage := makeTestStage(true, []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, []string{"goga:apply"})

	result, err := s.EvaluateStage(context.Background(), stage, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.CanExecuteAutonomously {
		t.Error("expected autonomous=false")
	}
}

func TestSupervisor_RunnerError_ReturnsError(t *testing.T) {
	runner := &mockJSONRunner{err: errors.New("network timeout")}
	s := NewSupervisor(runner)
	stage := makeTestStage(true, []flow.AgentType{flow.AgentPlanning}, nil)

	_, err := s.EvaluateStage(context.Background(), stage, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestSupervisor_BadJSON_ReturnsError(t *testing.T) {
	runner := &mockJSONRunner{response: []byte(`not json`)}
	s := NewSupervisor(runner)
	_, err := s.EvaluateStage(context.Background(), makeTestStage(true, nil, nil), "")
	if err == nil {
		t.Fatal("expected error for bad JSON")
	}
}

func TestSupervisor_AutonomousWrongPhases_ReturnsError(t *testing.T) {
	// Autonomous-решение обязано рекомендовать ровно ["autonomous_execution"].
	runner := &mockJSONRunner{
		response: []byte(`{"can_execute_autonomously":true,"reason":"x","recommended_phases":["deploy"]}`),
	}
	s := NewSupervisor(runner)
	stage := makeTestStage(true, []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, nil)
	_, err := s.EvaluateStage(context.Background(), stage, "")
	if err == nil {
		t.Fatal("expected error for autonomous decision with non-autonomous phases")
	}
}

func TestSupervisor_StandardAcceptsMalformedPhases(t *testing.T) {
	// Standard-решение с malformed phases (LLM написал "planning implementation"
	// одной строкой — артефакт рендера слайса) НЕ должно фейлиться: phases advisory,
	// DetermineStagePhases всё равно вернёт base. Прежний строгий контроль давал
	// ложный fallback и прятал валидное решение из лога/UI.
	runner := &mockJSONRunner{
		response: []byte(`{"can_execute_autonomously":false,"reason":"exploratory","recommended_phases":["planning implementation"]}`),
	}
	s := NewSupervisor(runner)
	stage := makeTestStage(true, []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, nil)
	result, err := s.EvaluateStage(context.Background(), stage, "")
	if err != nil {
		t.Fatalf("standard decision must not fail on malformed phases: %v", err)
	}
	if result.CanExecuteAutonomously {
		t.Error("expected autonomous=false")
	}
}

// flakyJSONRunner — Runner, у которого первые failN вызовов RunJSONQuery
// возвращают err, а последующие — response. Считает вызовы.
type flakyJSONRunner struct {
	failN    int
	err      error
	response []byte
	calls    int
}

func (m *flakyJSONRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	m.calls++
	if m.calls <= m.failN {
		return nil, m.err
	}
	return m.response, nil
}
func (m *flakyJSONRunner) RunPlanning(_ context.Context, _, _, _, _ string) error { return nil }
func (m *flakyJSONRunner) RunAgent(_ context.Context, _, _, _, _ string) error    { return nil }

const standardDecision = `{"can_execute_autonomously":false,"reason":"needs planning","recommended_phases":["planning","implementation"]}`

func TestSupervisor_RetriesTransientError(t *testing.T) {
	// Transient-ошибки (529/overloaded) переживаются ретраем с backoff,
	// как у stage-агентов (retry.go), а не немедленным фолбэком на базовые фазы.
	origBackoff, origMax := RetryBackoff, MaxRetries
	RetryBackoff, MaxRetries = time.Millisecond, 3
	t.Cleanup(func() { RetryBackoff, MaxRetries = origBackoff, origMax })

	runner := &flakyJSONRunner{
		failN:    2,
		err:      errors.New("API Error: 529 overloaded"),
		response: []byte(standardDecision),
	}
	s := NewSupervisor(runner)
	stage := makeTestStage(true, []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, nil)

	result, err := s.EvaluateStage(context.Background(), stage, "")
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if runner.calls != 3 {
		t.Errorf("runner calls = %d, want 3 (2 failures + 1 success)", runner.calls)
	}
	if result.CanExecuteAutonomously {
		t.Error("expected autonomous=false")
	}
}

func TestSupervisor_RetriesExhausted(t *testing.T) {
	origBackoff, origMax := RetryBackoff, MaxRetries
	RetryBackoff, MaxRetries = time.Millisecond, 2
	t.Cleanup(func() { RetryBackoff, MaxRetries = origBackoff, origMax })

	runner := &flakyJSONRunner{failN: int(^uint(0) >> 1), err: errors.New("API Error: 529 overloaded")}
	s := NewSupervisor(runner)
	stage := makeTestStage(true, []flow.AgentType{flow.AgentPlanning}, nil)

	_, err := s.EvaluateStage(context.Background(), stage, "")
	if err == nil {
		t.Fatal("expected retries-exhausted error, got nil")
	}
	if runner.calls != 3 {
		t.Errorf("runner calls = %d, want 3 (initial + MaxRetries=2)", runner.calls)
	}
}

func TestSupervisor_RetrySnapshotAtNew(t *testing.T) {
	// RetryBackoff/MaxRetries фиксируются при NewSupervisor (как у Orchestrator,
	// см. cd7c65f): мутация package var ПОСЛЕ New не влияет на работающий
	// супервизор — иначе data race с restore в t.Cleanup из горутины супервизора.
	origBackoff, origMax := RetryBackoff, MaxRetries
	RetryBackoff, MaxRetries = time.Millisecond, 3

	runner := &flakyJSONRunner{
		failN:    1,
		err:      errors.New("API Error: 529 overloaded"),
		response: []byte(standardDecision),
	}
	s := NewSupervisor(runner)

	// После New значения «портятся»: если EvaluateStage читает package var,
	// MaxRetries=0 даст exhausted сразу, а RetryBackoff=1h — зависание (страхует ctx).
	RetryBackoff, MaxRetries = time.Hour, 0
	t.Cleanup(func() { RetryBackoff, MaxRetries = origBackoff, origMax })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stage := makeTestStage(true, []flow.AgentType{flow.AgentPlanning}, nil)

	if _, err := s.EvaluateStage(ctx, stage, ""); err != nil {
		t.Fatalf("supervisor must use values snapshotted at New, got: %v", err)
	}
	if runner.calls != 2 {
		t.Errorf("runner calls = %d, want 2 (1 failure + 1 success)", runner.calls)
	}
}

// TestSupervisor_ClaudeEnvelope проверяет, что parseDecision корректно
// извлекает decision JSON из envelope'а команды claude (-p --output-format json),
// где ответ обёрнут в {"result":"..."} и может содержать markdown-фенсы.
// Envelope собран через json.Marshal, чтобы внутренние кавычки и переводы строк
// были экранированы ровно так, как их отдаёт настоящий claude.
func TestSupervisor_ClaudeEnvelope(t *testing.T) {
	inner := "```json\n" + `{"can_execute_autonomously":true,"reason":"skill covers it","recommended_phases":["autonomous_execution"]}` + "\n```"
	envelopeBytes, err := json.Marshal(struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}{Type: "result", Subtype: "success", Result: inner, IsError: false})
	if err != nil {
		t.Fatalf("build envelope: %v", err)
	}
	runner := &mockJSONRunner{response: envelopeBytes}
	s := NewSupervisor(runner)
	stage := makeTestStage(true, []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, []string{"goga:apply"})

	result, err := s.EvaluateStage(context.Background(), stage, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.CanExecuteAutonomously {
		t.Error("expected autonomous=true")
	}
	if len(result.RecommendedPhases) != 1 || result.RecommendedPhases[0] != "autonomous_execution" {
		t.Errorf("unexpected phases: %v", result.RecommendedPhases)
	}
}

func TestSupervisor_ClaudeArray(t *testing.T) {
	// claude --output-format json (актуальные версии) = массив событий; decision
	// лежит в result последнего element с type=result.
	decision := `{"can_execute_autonomously":false,"reason":"needs planning","recommended_phases":["planning","implementation"]}`
	arrBytes, err := json.Marshal([]struct {
		Type   string `json:"type"`
		Result string `json:"result,omitempty"`
	}{
		{Type: "system"},
		{Type: "assistant"},
		{Type: "result", Result: decision},
	})
	if err != nil {
		t.Fatalf("build array: %v", err)
	}
	runner := &mockJSONRunner{response: arrBytes}
	s := NewSupervisor(runner)
	stage := makeTestStage(true, []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, nil)

	result, err := s.EvaluateStage(context.Background(), stage, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.CanExecuteAutonomously {
		t.Error("expected autonomous=false")
	}
	if len(result.RecommendedPhases) != 2 || result.RecommendedPhases[0] != "planning" {
		t.Errorf("unexpected phases: %v", result.RecommendedPhases)
	}
}
