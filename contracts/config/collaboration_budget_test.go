package config_test

import (
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/budget"
)

func TestProfileBudget_ParsesAndReplacesAsWhole(t *testing.T) {
	global := "collaboration:\n  profiles:\n    qa:\n      command: /abs/x.sh\n      budget: {tokens: 1m, money: 2$}\n"
	project := "collaboration:\n  profiles:\n    qa:\n      command: /abs/y.sh\n      budget: {tokens: 500k, stop_policy: info}\n"
	cfg, err := loadModeLayers(t, global, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Collaboration.Profiles["qa"].Budget; got == nil || *got != (budget.Spec{Tokens: 1_000_000, MoneyUSD: 2, StopPolicy: budget.PolicyHard}) {
		t.Fatalf("global budget = %+v", got)
	}
	cfg, err = loadModeLayers(t, global, project)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Collaboration.Profiles["qa"].Budget; got == nil || *got != (budget.Spec{Tokens: 500_000, StopPolicy: budget.PolicyInfo}) {
		t.Fatalf("project budget = %+v, want it to replace the global one", got)
	}
}

func TestProfileBudget_AbsentIsNil(t *testing.T) {
	cfg, err := loadModeLayers(t, "", "collaboration:\n  profiles:\n    qa:\n      command: /abs/x.sh\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collaboration.Profiles["qa"].Budget != nil {
		t.Fatal("budget must stay nil when not declared")
	}
}

func TestProfileBudget_RejectsInvalid(t *testing.T) {
	for name, val := range map[string]string{
		"null":        "null",
		"bare":        "",
		"empty":       "{}",
		"policy only": "{stop_policy: hard}",
		"zero":        "{money: 0}",
		"unknown key": "{tokens: 1, cap: 2}",
		"bad policy":  "{tokens: 1, stop_policy: soft}",
		"duplicate":   "{money: 1, money: 2}",
	} {
		doc := "collaboration:\n  profiles:\n    qa:\n      command: /abs/x.sh\n      budget: " + val + "\n"
		if _, err := loadModeLayers(t, "", doc); err == nil {
			t.Errorf("%s: load succeeded, want error", name)
		}
	}
}

func TestProfileBudget_NullErrorNamesTheKey(t *testing.T) {
	_, err := loadModeLayers(t, "", "collaboration:\n  profiles:\n    qa:\n      command: /abs/x.sh\n      budget:\n")
	if err == nil || !strings.Contains(err.Error(), "budget must not be null") {
		t.Fatalf("err = %v", err)
	}
}

// Null that arrives through a << merge or an alias is still null.
func TestProfileBudget_NullThroughMergeOrAliasIsRejected(t *testing.T) {
	for name, body := range map[string]string{
		"merge": "x: &n {budget: null}\ncollaboration:\n  profiles:\n    qa:\n      <<: *n\n      command: /abs/x.sh\n",
		"alias": "x: &n ~\ncollaboration:\n  profiles:\n    qa:\n      command: /abs/x.sh\n      budget: *n\n",
	} {
		if _, err := loadModeLayers(t, "", body); err == nil || !strings.Contains(err.Error(), "budget must not be null") {
			t.Errorf("%s: err = %v, want budget null error", name, err)
		}
	}
}

// The budget key counts as yaml.v3 decodes it, in either config layer.
func TestProfileBudget_YAMLKeyIdentity(t *testing.T) {
	const profile = "collaboration:\n  profiles:\n    qa:\n      command: /abs/x.sh\n"
	rejected := map[string]string{
		"key alias":               "x: &k budget\n" + profile + "      *k : null\n",
		"binary key":              profile + "      !!binary YnVkZ2V0: null\n",
		"merge tag on the key":    profile + "      !!merge budget: null\n",
		"merge with key alias":    "x: &k budget\n" + profile + "      <<: {*k : null}\n",
		"first merge source null": profile + "      <<: [{budget: null}, {budget: {tokens: 1}}]\n",
	}
	for name, doc := range rejected {
		for layer, layers := range map[string][2]string{"global": {doc, ""}, "project": {"", doc}} {
			if _, err := loadModeLayers(t, layers[0], layers[1]); err == nil || !strings.Contains(err.Error(), "budget must not be null") {
				t.Errorf("%s in %s config: err = %v, want budget null error", name, layer, err)
			}
		}
	}
	// A project that replaces the profile does not hide an invalid global declaration.
	if _, err := loadModeLayers(t, rejected["key alias"], profile+"      budget: {tokens: 1}\n"); err == nil {
		t.Error("invalid global budget accepted because the project replaces the profile")
	}

	accepted := map[string]string{
		"key alias":                   "x: &k budget\n" + profile + "      *k : {tokens: 1}\n",
		"direct over merged null":     profile + "      <<: {budget: null}\n      budget: {tokens: 1}\n",
		"tagged key over merged null": profile + "      <<: {budget: null}\n      !!binary YnVkZ2V0: {tokens: 1}\n",
		"first merge source valid":    profile + "      <<: [{budget: {tokens: 1}}, {budget: null}]\n",
	}
	for name, doc := range accepted {
		for layer, layers := range map[string][2]string{"global": {doc, ""}, "project": {"", doc}} {
			cfg, err := loadModeLayers(t, layers[0], layers[1])
			if err != nil {
				t.Errorf("%s in %s config: %v", name, layer, err)
				continue
			}
			if got := cfg.Collaboration.Profiles["qa"].Budget; got == nil || got.Tokens != 1 {
				t.Errorf("%s in %s config: budget = %+v, want 1 token", name, layer, got)
			}
		}
	}
}
