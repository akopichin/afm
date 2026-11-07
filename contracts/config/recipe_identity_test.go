package config_test

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/config"
)

func sampleRecipe() config.AgentRecipe {
	return config.AgentRecipe{
		Type: "claude", Model: "glm-5", URL: "https://gw.example/v1",
		SystemPrompt: "file:/prompts/sp.md", MaxTurns: 7, Reasoning: "high",
		Auth: config.RecipeAuth{From: "env:GLM_KEY", To: "env:ANTHROPIC_API_KEY"},
	}
}

func TestAgentRecipeIdentity_StableAndHex(t *testing.T) {
	a, b := sampleRecipe().Identity(), sampleRecipe().Identity()
	if a != b {
		t.Fatalf("equal recipes gave different identities: %q vs %q", a, b)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(a) {
		t.Fatalf("identity %q is not a 64-char lowercase hex digest", a)
	}
}

// Каждое поле рецепта (включая вложенные) меняет identity: иначе смена модели, адреса или аккаунта
// после создания задачи осталась бы незамеченной.
func TestAgentRecipeIdentity_EveryFieldChangesIt(t *testing.T) {
	base := sampleRecipe().Identity()
	rt := reflect.TypeOf(config.AgentRecipe{})
	var check func(rt reflect.Type, path []int, prefix string)
	check = func(rt reflect.Type, path []int, prefix string) {
		for i := 0; i < rt.NumField(); i++ {
			cur := append(append([]int{}, path...), i)
			name := prefix + rt.Field(i).Name
			if rt.Field(i).Type.Kind() == reflect.Struct {
				check(rt.Field(i).Type, cur, name+".")
				continue
			}
			r := sampleRecipe()
			f := reflect.ValueOf(&r).Elem().FieldByIndex(cur)
			switch f.Kind() {
			case reflect.String:
				f.SetString(f.String() + "x")
			case reflect.Int:
				f.SetInt(f.Int() + 1)
			default:
				t.Fatalf("field %s has unsupported kind %s: extend the test", name, f.Kind())
			}
			if r.Identity() == base {
				t.Errorf("changing %s did not change the identity", name)
			}
		}
	}
	check(rt, nil, "")
}

func TestAgentRecipeIdentity_DoesNotContainFieldValues(t *testing.T) {
	id := sampleRecipe().Identity()
	for _, v := range []string{"glm-5", "GLM_KEY", "gw.example", "sp.md"} {
		if strings.Contains(id, v) {
			t.Errorf("identity %q leaks field value %q", id, v)
		}
	}
}
