package flow

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// verifyHolder — обёртка для декодирования одного поля verify изолированно от
// остального Stage (Stage.Verify ещё не существует на момент V1.1).
type verifyHolder struct {
	Verify VerifySpec `yaml:"verify,omitempty"`
}

// parseVerify декодирует YAML-фрагмент "verify: ..." и, если декодирование
// прошло успешно, сразу прогоняет validate(stageID) — так тестируется вся
// цепочка целиком (ровно то, что делает Flow.validate() в V1.2).
func parseVerify(t *testing.T, yamlSrc, stageID string) (VerifySpec, error) {
	t.Helper()
	var h verifyHolder
	if err := yaml.Unmarshal([]byte(yamlSrc), &h); err != nil {
		return VerifySpec{}, err
	}
	if err := h.Verify.validate(stageID); err != nil {
		return VerifySpec{}, err
	}
	return h.Verify, nil
}

func TestVerifySpec_Scalar(t *testing.T) {
	spec, err := parseVerify(t, `verify: "true"`, "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(spec.Steps) != 1 {
		t.Fatalf("Steps = %v, want 1 step", spec.Steps)
	}
	st := spec.Steps[0]
	if st.Kind != VerifyShell || st.Run != "true" {
		t.Errorf("step = %+v, want shell run=true", st)
	}
	if !spec.fromScalar {
		t.Error("fromScalar = false, want true for a scalar verify")
	}
}

func TestVerifySpec_EmptyScalarIsEmptySpec(t *testing.T) {
	spec, err := parseVerify(t, `verify: ""`, "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !spec.IsEmpty() {
		t.Errorf("spec = %+v, want empty", spec)
	}
}

func TestVerifySpec_RunObjectMatchesScalarButNotFromScalar(t *testing.T) {
	spec, err := parseVerify(t, "verify:\n  run: \"true\"\n", "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(spec.Steps) != 1 || spec.Steps[0].Kind != VerifyShell || spec.Steps[0].Run != "true" {
		t.Fatalf("step = %+v, want shell run=true", spec.Steps)
	}
	if spec.fromScalar {
		t.Error("fromScalar = true, want false for an object form")
	}
}

func TestVerifySpec_CommandObject(t *testing.T) {
	spec, err := parseVerify(t, "verify:\n  command: codex\n  prompt: \"проверь тесты\"\n", "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(spec.Steps) != 1 {
		t.Fatalf("Steps = %v, want 1 step", spec.Steps)
	}
	st := spec.Steps[0]
	if st.Kind != VerifyAgent || st.Command != "codex" || st.Prompt != "проверь тесты" {
		t.Errorf("step = %+v, want agent command=codex", st)
	}
}

func TestVerifySpec_ListOfSteps(t *testing.T) {
	src := "verify:\n" +
		"  - run: \"go test ./...\"\n" +
		"    timeout: 5m\n" +
		"  - command: codex\n" +
		"    timeout: 15m\n" +
		"    prompt: \"проверь покрытие\"\n"
	spec, err := parseVerify(t, src, "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(spec.Steps) != 2 {
		t.Fatalf("Steps = %+v, want 2 steps", spec.Steps)
	}
	first, second := spec.Steps[0], spec.Steps[1]
	if first.Kind != VerifyShell || first.Run != "go test ./..." || first.Timeout != 5*time.Minute {
		t.Errorf("first step = %+v", first)
	}
	if second.Kind != VerifyAgent || second.Command != "codex" || second.Timeout != 15*time.Minute || second.Prompt != "проверь покрытие" {
		t.Errorf("second step = %+v", second)
	}
}

func TestVerifySpec_RunAndCommandMutuallyExclusive(t *testing.T) {
	_, err := parseVerify(t, "verify:\n  run: \"true\"\n  command: codex\n", "s1")
	if err == nil {
		t.Fatal("expected error for run+command together")
	}
	want := `stage "s1": verify[1]: run and command are mutually exclusive`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestVerifySpec_RunAndCommandMutuallyExclusive_ListIndexIsOneBased(t *testing.T) {
	src := "verify:\n" +
		"  - run: \"true\"\n" +
		"  - run: \"echo x\"\n" +
		"    command: codex\n"
	_, err := parseVerify(t, src, "s2")
	if err == nil {
		t.Fatal("expected error for run+command together")
	}
	want := `stage "s2": verify[2]: run and command are mutually exclusive`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestVerifySpec_AgentObjectWithoutCommand(t *testing.T) {
	_, err := parseVerify(t, "verify:\n  prompt: \"проверь тесты\"\n", "s1")
	if err == nil {
		t.Fatal("expected error for an object without run or command")
	}
}

func TestVerifySpec_AgentFieldRejectedMentionsCommand(t *testing.T) {
	_, err := parseVerify(t, "verify:\n  agent: codex\n", "s1")
	if err == nil {
		t.Fatal("expected error for unknown field \"agent\"")
	}
	if !strings.Contains(err.Error(), "agent") || !strings.Contains(err.Error(), "command") {
		t.Errorf("error = %q, want it to mention both \"agent\" and \"command\"", err.Error())
	}
}

// TestVerifySpec_OtherUnknownFieldsRejected покрывает список полей из брифа,
// которые когда-то рассматривались для verify-контракта, но не вошли в него.
func TestVerifySpec_OtherUnknownFieldsRejected(t *testing.T) {
	for _, field := range []string{"verify_agent", "model", "url", "token", "extra_args", "on_fail", "mode", "required", "parallel"} {
		t.Run(field, func(t *testing.T) {
			src := "verify:\n  run: \"true\"\n  " + field + ": x\n"
			_, err := parseVerify(t, src, "s1")
			if err == nil {
				t.Fatalf("expected error for unknown field %q", field)
			}
		})
	}
}

func TestVerifySpec_ScalarCodexStaysShell(t *testing.T) {
	spec, err := parseVerify(t, `verify: "codex"`, "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(spec.Steps) != 1 || spec.Steps[0].Kind != VerifyShell || spec.Steps[0].Run != "codex" {
		t.Errorf("spec = %+v, want a single shell step with Run=\"codex\"", spec.Steps)
	}
}

func TestVerifySpec_NegativeTimeoutRejected(t *testing.T) {
	_, err := parseVerify(t, "verify:\n  run: \"true\"\n  timeout: -5m\n", "s1")
	if err == nil {
		t.Fatal("expected error for a negative timeout")
	}
}

func TestVerifySpec_NestedListRejected(t *testing.T) {
	src := "verify:\n" +
		"  - - run: \"true\"\n"
	_, err := parseVerify(t, src, "s1")
	if err == nil {
		t.Fatal("expected error for a nested list")
	}
}

func TestVerifySpec_EmptyObjectRejected(t *testing.T) {
	_, err := parseVerify(t, "verify: {}\n", "s1")
	if err == nil {
		t.Fatal("expected error for an empty verify object")
	}
}

func TestVerifySpec_EmptyListRejected(t *testing.T) {
	_, err := parseVerify(t, "verify: []\n", "s1")
	if err == nil {
		t.Fatal("expected error for an empty verify list")
	}
}

func TestVerifySpec_IsEmpty(t *testing.T) {
	var zero VerifySpec
	if !zero.IsEmpty() {
		t.Error("zero-value VerifySpec should be empty")
	}
	spec, err := parseVerify(t, `verify: "true"`, "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.IsEmpty() {
		t.Error("non-empty spec reported as empty")
	}
}

func TestVerifySpec_MarshalRoundTrip(t *testing.T) {
	// Скаляр маршалится обратно скаляром (fromScalar=true), а не объектом —
	// это нужно afm init (генерирует flow.Stage в памяти и сериализует в
	// flow.yaml, см. cmd/afm/init_archetype_test.go).
	spec, err := parseVerify(t, `verify: "go test ./..."`, "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out, err := yaml.Marshal(verifyHolder{Verify: spec})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := parseVerify(t, string(out), "s1")
	if err != nil {
		t.Fatalf("re-parse marshalled verify: %v (yaml: %s)", err, out)
	}
	if len(back.Steps) != 1 || back.Steps[0].Run != "go test ./..." {
		t.Errorf("round-trip mismatch: %+v", back.Steps)
	}
	if !strings.Contains(string(out), "go test ./...") {
		t.Errorf("marshalled yaml = %q, want it to contain the command", out)
	}
}

func TestVerifySpec_MarshalOmitsWhenEmpty(t *testing.T) {
	out, err := yaml.Marshal(verifyHolder{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "verify") {
		t.Errorf("marshalled yaml = %q, want no verify key for an empty spec", out)
	}
}

// TestVerifySpec_ContainerForm — новая объектная форма {steps: [...], max_failures: N}:
// шаги разбираются как список, max_failures становится указателем.
func TestVerifySpec_ContainerForm(t *testing.T) {
	src := "verify:\n" +
		"  steps:\n" +
		"    - run: \"go test ./...\"\n" +
		"    - command: codex\n" +
		"      prompt: \"проверь покрытие\"\n" +
		"  max_failures: 3\n"
	spec, err := parseVerify(t, src, "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(spec.Steps) != 2 {
		t.Fatalf("Steps = %+v, want 2 steps", spec.Steps)
	}
	if spec.Steps[0].Kind != VerifyShell || spec.Steps[0].Run != "go test ./..." {
		t.Errorf("first step = %+v", spec.Steps[0])
	}
	if spec.Steps[1].Kind != VerifyAgent || spec.Steps[1].Command != "codex" || spec.Steps[1].Prompt != "проверь покрытие" {
		t.Errorf("second step = %+v", spec.Steps[1])
	}
	if spec.MaxFailures == nil || *spec.MaxFailures != 3 {
		t.Errorf("MaxFailures = %v, want 3", spec.MaxFailures)
	}
	if spec.fromScalar {
		t.Error("fromScalar = true, want false for a container form")
	}
}

// TestVerifySpec_ContainerFormWithoutMaxFailures — steps без max_failures:
// MaxFailures остаётся nil (наследование из config).
func TestVerifySpec_ContainerFormWithoutMaxFailures(t *testing.T) {
	src := "verify:\n" +
		"  steps:\n" +
		"    - run: \"true\"\n"
	spec, err := parseVerify(t, src, "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(spec.Steps) != 1 || spec.Steps[0].Run != "true" {
		t.Fatalf("Steps = %+v", spec.Steps)
	}
	if spec.MaxFailures != nil {
		t.Errorf("MaxFailures = %v, want nil", spec.MaxFailures)
	}
}

// TestVerifySpec_ContainerFormZeroMaxFailures — max_failures: 0 разрешён
// (строгий режим: первое отклонение сразу проваливает стадию).
func TestVerifySpec_ContainerFormZeroMaxFailures(t *testing.T) {
	src := "verify:\n  steps:\n    - run: \"true\"\n  max_failures: 0\n"
	spec, err := parseVerify(t, src, "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.MaxFailures == nil || *spec.MaxFailures != 0 {
		t.Errorf("MaxFailures = %v, want 0", spec.MaxFailures)
	}
}

func TestVerifySpec_ContainerNegativeMaxFailuresRejected(t *testing.T) {
	src := "verify:\n  steps:\n    - run: \"true\"\n  max_failures: -1\n"
	_, err := parseVerify(t, src, "s1")
	if err == nil {
		t.Fatal("expected error for a negative max_failures")
	}
	if !strings.Contains(err.Error(), "max_failures") {
		t.Errorf("error = %q, want it to mention max_failures", err.Error())
	}
}

func TestVerifySpec_ContainerEmptyStepsRejected(t *testing.T) {
	src := "verify:\n  steps: []\n  max_failures: 2\n"
	if _, err := parseVerify(t, src, "s1"); err == nil {
		t.Fatal("expected error for an empty steps list")
	}
}

func TestVerifySpec_ContainerStepsNotSequenceRejected(t *testing.T) {
	src := "verify:\n  steps: nope\n"
	if _, err := parseVerify(t, src, "s1"); err == nil {
		t.Fatal("expected error for a non-sequence steps value")
	}
}

func TestVerifySpec_ContainerUnknownKeyRejected(t *testing.T) {
	src := "verify:\n  steps:\n    - run: \"true\"\n  bogus: x\n"
	_, err := parseVerify(t, src, "s1")
	if err == nil {
		t.Fatal("expected error for an unknown container key")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error = %q, want it to mention the unknown key", err.Error())
	}
}

func TestVerifySpec_ContainerStepsMixedWithRunRejected(t *testing.T) {
	src := "verify:\n  steps:\n    - run: \"true\"\n  run: \"echo x\"\n"
	if _, err := parseVerify(t, src, "s1"); err == nil {
		t.Fatal("expected error for run mixed with steps at the container level")
	}
}

// TestVerifySpec_MaxFailuresWithoutStepsRejected — max_failures в одиночной
// (не container) форме — это неизвестное поле шага, а не бюджет.
func TestVerifySpec_MaxFailuresWithoutStepsRejected(t *testing.T) {
	src := "verify:\n  max_failures: 2\n"
	if _, err := parseVerify(t, src, "s1"); err == nil {
		t.Fatal("expected error for max_failures without steps")
	}
}

// TestVerifySpec_ContainerMarshalRoundTrip — round-trip сохраняет max_failures.
func TestVerifySpec_ContainerMarshalRoundTrip(t *testing.T) {
	src := "verify:\n" +
		"  steps:\n" +
		"    - run: \"go test ./...\"\n" +
		"    - command: codex\n" +
		"  max_failures: 4\n"
	spec, err := parseVerify(t, src, "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out, err := yaml.Marshal(verifyHolder{Verify: spec})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := parseVerify(t, string(out), "s1")
	if err != nil {
		t.Fatalf("re-parse marshalled verify: %v (yaml: %s)", err, out)
	}
	if len(back.Steps) != 2 {
		t.Fatalf("round-trip steps mismatch: %+v", back.Steps)
	}
	if back.MaxFailures == nil || *back.MaxFailures != 4 {
		t.Errorf("round-trip MaxFailures = %v, want 4 (yaml: %s)", back.MaxFailures, out)
	}
}
