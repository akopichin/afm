package flow

import "testing"

// minimalFlowWithStageID строит минимальный валидный (во всём, кроме id)
// флоу с одной нескриптовой стадией, чтобы изолированно проверить только
// safePathComponent-проверку id. Стадия несёт planning+implementation,
// чтобы не упасть на более раннем "must have planning agent" чеке.
func minimalFlowWithStageID(id string) *Flow {
	return &Flow{
		Name: "f",
		Stages: []Stage{
			{ID: id, Name: "stage", Agents: []AgentType{AgentPlanning, AgentImplementation}},
		},
	}
}

// minimalFlowWithReflect строит минимальный валидный флоу с включённой
// памятью (Memory.Path непусто) и одной нескриптовой стадией с reflect,
// чтобы изолированно проверить только path-safety проверку reflect.file.
func minimalFlowWithReflect(reflectFile string) *Flow {
	return &Flow{
		Name:   "f",
		Memory: MemoryConfig{Path: "docs/memory"},
		Stages: []Stage{
			{
				ID:     "s",
				Name:   "stage",
				Agents: []AgentType{AgentPlanning, AgentImplementation},
				Reflect: &Reflect{
					File: reflectFile,
					Mode: ReflectModeRW,
				},
			},
		},
	}
}

func TestValidate_StageIDPathSafety(t *testing.T) {
	for _, id := range []string{"a/b", `a\b`, ".", "..", "", "../x"} {
		f := minimalFlowWithStageID(id)
		if err := f.validate(); err == nil {
			t.Errorf("stage id %q must be rejected", id)
		}
	}
}

func TestValidate_ReflectFilePathSafety(t *testing.T) {
	for _, rf := range []string{"../escape.md", "/abs/mem.md", "a/../../b.md", "memory.md", "./memory.md", "sub/../memory.md"} {
		f := minimalFlowWithReflect(rf)
		if err := f.validate(); err == nil {
			t.Errorf("reflect.file %q must be rejected", rf)
		}
	}
	if err := minimalFlowWithReflect("sub/dir/stage.md").validate(); err != nil {
		t.Errorf("safe reflect.file rejected: %v", err)
	}
}
