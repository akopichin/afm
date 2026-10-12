package config

import "testing"

// clampCases enumerates every canonical × mechanism cell from the plan's clamp
// table. Each entry is the exact value afm must forward to that mechanism.
func TestResolveReasoningClampTable(t *testing.T) {
	cases := []struct {
		canonical string
		codex     string
		claude    string
		openai    string
	}{
		{"minimal", "minimal", "low", "minimal"},
		{"low", "low", "low", "low"},
		{"medium", "medium", "medium", "medium"},
		{"high", "high", "high", "high"},
		{"xhigh", "xhigh", "xhigh", "high"},
		{"max", "xhigh", "max", "high"},
	}
	for _, c := range cases {
		for _, m := range []struct {
			mechanism string
			want      string
		}{
			{MechanismCodex, c.codex},
			{MechanismClaude, c.claude},
			{MechanismOpenAI, c.openai},
		} {
			got, warn := ResolveReasoning(m.mechanism, c.canonical)
			if got != m.want || warn {
				t.Errorf("ResolveReasoning(%q, %q) = (%q, %v), want (%q, false)",
					m.mechanism, c.canonical, got, warn, m.want)
			}
		}
	}
}

func TestResolveReasoningPassthrough(t *testing.T) {
	for _, m := range []string{MechanismCodex, MechanismClaude, MechanismOpenAI} {
		got, warn := ResolveReasoning(m, "very-good")
		if got != "very-good" || !warn {
			t.Errorf("ResolveReasoning(%q, %q) = (%q, %v), want (%q, true)",
				m, "very-good", got, warn, "very-good")
		}
	}
}

func TestResolveReasoningEmpty(t *testing.T) {
	for _, m := range []string{MechanismCodex, MechanismClaude, MechanismOpenAI} {
		got, warn := ResolveReasoning(m, "")
		if got != "" || warn {
			t.Errorf("ResolveReasoning(%q, \"\") = (%q, %v), want (\"\", false)", m, got, warn)
		}
	}
}

func TestMechanismForRecipeType(t *testing.T) {
	cases := []struct {
		recipeType string
		mechanism  string
		ok         bool
	}{
		{"", MechanismClaude, true},
		{"claude", MechanismClaude, true},
		{"codex", MechanismCodex, true},
		{"openai", MechanismOpenAI, true},
		{"openai-agent", MechanismOpenAI, true},
		{"cursor", "", false},
		{"nonsense", "", false},
	}
	for _, c := range cases {
		m, ok := MechanismForRecipeType(c.recipeType)
		if m != c.mechanism || ok != c.ok {
			t.Errorf("MechanismForRecipeType(%q) = (%q, %v), want (%q, %v)",
				c.recipeType, m, ok, c.mechanism, c.ok)
		}
	}
}
