package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsAuto(t *testing.T) {
	if !(&Stage{Agents: []AgentType{AgentAuto}}).IsAuto() {
		t.Error("[auto] should be IsAuto")
	}
	for _, s := range []*Stage{
		{Agents: []AgentType{AgentPlanning}},
		{Agents: []AgentType{AgentAuto, AgentPlanning}},
		{Agents: nil},
	} {
		if s.IsAuto() {
			t.Errorf("%v should not be IsAuto", s.Agents)
		}
	}
}

func TestAutoStage_NoPlanningNoImplAgent(t *testing.T) {
	s := &Stage{Agents: []AgentType{AgentAuto}}
	if s.NeedsPlanning() {
		t.Error("auto stage must not need planning")
	}
	if s.HasAgent(AgentPlanning) || s.HasAgent(AgentImplementation) || s.HasAgent(AgentReview) {
		t.Error("auto stage must not report having planning/implementation/review agents")
	}
	if s.ImplAgent() == AgentAuto {
		t.Error("ImplAgent must never return the literal auto (would be used as a command)")
	}
}

// writeFlow — хелпер: пишет YAML во временный файл и парсит.
func writeFlow(t *testing.T, yaml string) (*Flow, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "flow.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	return ParseFile(p)
}

func TestParse_AutoStageValid(t *testing.T) {
	_, err := writeFlow(t, `
name: f
stages:
  - id: a
    agents: [auto]
`)
	if err != nil {
		t.Fatalf("valid [auto] stage rejected: %v", err)
	}
}

func TestParse_AutoMustBeSoleAgent(t *testing.T) {
	_, err := writeFlow(t, `
name: f
stages:
  - id: a
    agents: [auto, planning]
`)
	if err == nil || !strings.Contains(err.Error(), "only agent") {
		t.Fatalf("auto+planning: want 'only agent' error, got %v", err)
	}
}
