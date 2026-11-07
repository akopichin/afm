package schema

import (
	"slices"
	"strings"
	"testing"
)

const (
	neutralResult = `{"schema_version":1,"kind":"result","summary":"logs collected"}`
	passResult    = `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok"}` //nolint:gosec // test fixture, not a credential
	delegateTurn  = `{"schema_version":1,"kind":"delegate","children":[{"profile_id":"p","title":"t","instruction":"i"}]}`
)

func resultRequired(t *testing.T, contract Contract) []any {
	t.Helper()
	definitions := decodeObject(t, JSON(contract))["definitions"].(map[string]any)
	required, _ := definitions["result"].(map[string]any)["required"].([]any)
	return required
}

func TestAssistantResultVerdictIsOptionalOnlyInAdvisoryContracts(t *testing.T) {
	for _, tc := range []struct {
		contract Contract
		want     bool
	}{
		{AssistantResult, false},
		{AssistantResultOnly, false},
		{AssistantResultRequired, true},
		{AssistantResultOnlyRequired, true},
	} {
		if got := slices.Contains(resultRequired(t, tc.contract), any("verdict")); got != tc.want {
			t.Errorf("%s: verdict required = %v, want %v", tc.contract, got, tc.want)
		}
	}
}

func TestAssistantResultOnlyVariantsDropDelegation(t *testing.T) {
	for _, contract := range []Contract{AssistantResultOnly, AssistantResultOnlyRequired} {
		raw := JSON(contract)
		if strings.Contains(raw, `"delegate"`) || strings.Contains(raw, `"oneOf"`) {
			t.Errorf("%s still describes delegation: %s", contract, raw)
		}
	}
	for _, contract := range []Contract{AssistantResult, AssistantResultRequired} {
		if !strings.Contains(JSON(contract), `"delegate"`) {
			t.Errorf("%s lost the delegate form", contract)
		}
	}
}

func TestValidateAssistantResultVerdictPresence(t *testing.T) {
	cases := []struct {
		name     string
		contract Contract
		raw      string
		want     bool
	}{
		{"advisory takes neutral result", AssistantResult, neutralResult, true},
		{"advisory only takes neutral result", AssistantResultOnly, neutralResult, true},
		{"required rejects neutral result", AssistantResultRequired, neutralResult, false},
		{"required only rejects neutral result", AssistantResultOnlyRequired, neutralResult, false},
		{"required takes verdict", AssistantResultRequired, passResult, true},
		{"required only takes verdict", AssistantResultOnlyRequired, passResult, true},
		{"advisory takes verdict", AssistantResultOnly, passResult, true},
		{"required takes delegate", AssistantResultRequired, delegateTurn, true},
		{"advisory rejects empty verdict", AssistantResultOnly, `{"schema_version":1,"kind":"result","verdict":"","summary":"s"}`, false},
		{"advisory rejects null verdict", AssistantResultOnly, `{"schema_version":1,"kind":"result","verdict":null,"summary":"s"}`, false},
		{"advisory rejects unknown verdict", AssistantResultOnly, `{"schema_version":1,"kind":"result","verdict":"fine","summary":"s"}`, false},
		{"advisory keeps pass+blocker rule", AssistantResultOnly, `{"schema_version":1,"kind":"result","verdict":"pass","summary":"s","findings":[{"id":"f","severity":"blocker","detail":"d"}]}`, false},
		{"neutral result may carry a blocker", AssistantResultOnly, `{"schema_version":1,"kind":"result","summary":"s","findings":[{"id":"f","severity":"blocker","detail":"d"}]}`, true},
		{"advisory keeps summary required", AssistantResultOnly, `{"schema_version":1,"kind":"result"}`, false},
		{"advisory keeps unknown fields rejected", AssistantResultOnly, `{"schema_version":1,"kind":"result","summary":"s","extra":1}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Validate(tc.contract, []byte(tc.raw)) == nil; got != tc.want {
				t.Errorf("Validate(%s) accepted = %v, want %v", tc.contract, got, tc.want)
			}
		})
	}
}

func TestIsAssistantResult(t *testing.T) {
	for _, contract := range []Contract{AssistantResult, AssistantResultOnly, AssistantResultRequired, AssistantResultOnlyRequired} {
		if !IsAssistantResult(contract) {
			t.Errorf("IsAssistantResult(%s) = false", contract)
		}
	}
	for _, contract := range []Contract{VerifyResult, Question, "unknown"} {
		if IsAssistantResult(contract) {
			t.Errorf("IsAssistantResult(%s) = true", contract)
		}
	}
}

func TestNativeJSON_DelegateRequiredHasNoNativeVariant(t *testing.T) {
	for _, contract := range []Contract{AssistantResult, AssistantResultRequired} {
		if _, ok := NativeJSON(contract, DialectClaude); ok {
			t.Errorf("%s has a native variant", contract)
		}
	}
	for _, dialect := range nativeDialects {
		if _, ok := NativeJSON(AssistantResultOnlyRequired, dialect); !ok {
			t.Errorf("AssistantResultOnlyRequired has no %s variant", dialect)
		}
	}
}

func TestNativeJSON_VerdictOptionalInAdvisoryRequiredInRequired(t *testing.T) {
	advisory := decodeObject(t, mustNative(t, AssistantResultOnly, DialectClaude))
	if slices.Contains(advisory["required"].([]any), any("verdict")) {
		t.Errorf("claude advisory requires verdict: %v", advisory["required"])
	}
	required := decodeObject(t, mustNative(t, AssistantResultOnlyRequired, DialectClaude))
	if !slices.Contains(required["required"].([]any), any("verdict")) {
		t.Errorf("claude required does not require verdict: %v", required["required"])
	}

	strictAdvisory := decodeObject(t, mustNative(t, AssistantResultOnly, DialectStrict))["properties"].(map[string]any)["verdict"].(map[string]any)
	if !acceptsNull(strictAdvisory) || !slices.Contains(strictAdvisory["enum"].([]any), nil) {
		t.Errorf("strict advisory verdict = %v, want null as \"absent\"", strictAdvisory)
	}
	strictRequired := decodeObject(t, mustNative(t, AssistantResultOnlyRequired, DialectStrict))["properties"].(map[string]any)["verdict"].(map[string]any)
	if acceptsNull(strictRequired) {
		t.Errorf("strict required verdict accepts null: %v", strictRequired)
	}
}

func TestNormalizeNative_StrictNullVerdictBecomesNoVerdict(t *testing.T) {
	raw := `{"schema_version":1,"kind":"result","verdict":null,"summary":"logs collected","findings":null}`
	got := string(NormalizeNative(AssistantResultOnly, []byte(raw)))
	if strings.Contains(got, "verdict") {
		t.Fatalf("null verdict kept: %s", got)
	}
	if err := Validate(AssistantResultOnly, []byte(got)); err != nil {
		t.Fatalf("normalized answer rejected: %v", err)
	}
}

func TestNormalizeNative_RequiredKeepsNullVerdictForTheContractToReject(t *testing.T) {
	raw := `{"schema_version":1,"kind":"result","verdict":null,"summary":"s"}`
	got := NormalizeNative(AssistantResultOnlyRequired, []byte(raw))
	if err := Validate(AssistantResultOnlyRequired, got); err == nil {
		t.Fatalf("required contract accepted %s", got)
	}
}
