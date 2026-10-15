package assistants

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// SupportedSchemaVersion — единственная поддерживаемая версия envelope-схемы
// ответа помощника (schema/assistant-result.schema.json). Держи в синхроне с
// "const": 1 в схеме.
const SupportedSchemaVersion = 1

// Kind — обязательный дискриминатор ответа помощника. "result" и "delegate"
// структурно различны (verdict/findings против children/continuation) и
// сознательно НЕ различаются по одному лишь наличию полей — единственный
// надёжный способ отличить один от другого — это поле.
type Kind string

const (
	KindResult   Kind = "result"
	KindDelegate Kind = "delegate"
)

// Delegate — декодированное содержимое хода помощника с kind:"delegate".
// Полная семантика (авторизация/лимиты делегирования) фиксируется на этапе
// 4a; здесь — минимальная форма, уже достаточная для декодирования ответа
// модели (см. DelegateRequest/Continuation в types.go).
type Delegate struct {
	Summary      string
	Children     []DelegateRequest
	Continuation *Continuation
}

// Envelope — декодированный ответ помощника вместе с дискриминатором. Ровно
// одно из Result/Delegate непусто — какое именно, говорит Kind.
type Envelope struct {
	SchemaVersion int
	Kind          Kind
	Result        *Result
	Delegate      *Delegate
}

// resultEnvelope — JSON-форма kind:"result" (зеркало definitions.result схемы).
type resultEnvelope struct {
	SchemaVersion   int       `json:"schema_version"`
	Kind            Kind      `json:"kind"`
	Verdict         Verdict   `json:"verdict"`
	Summary         string    `json:"summary"`
	Findings        []Finding `json:"findings,omitempty"`
	Evidence        []string  `json:"evidence,omitempty"`
	ContextVersion  string    `json:"context_version,omitempty"`
	SuggestedChange string    `json:"suggested_change,omitempty"`
	Limitations     string    `json:"limitations,omitempty"`
}

// delegateEnvelope — JSON-форма kind:"delegate" (зеркало definitions.delegate схемы).
type delegateEnvelope struct {
	SchemaVersion int               `json:"schema_version"`
	Kind          Kind              `json:"kind"`
	Summary       string            `json:"summary,omitempty"`
	Children      []DelegateRequest `json:"children"`
	Continuation  *Continuation     `json:"continuation,omitempty"`
}

// discriminator — первый проход разбора: достаточно только kind, чтобы решить,
// какой из двух envelope-типов разбирать строго.
type discriminator struct {
	Kind Kind `json:"kind"`
}

// DecodeEnvelope строго разбирает сырой JSON-ответ помощника: отвергает
// неизвестные поля, хвостовой контент после документа, неподдерживаемую
// schema_version, отсутствующий/неизвестный kind и несогласованность
// verdict/findings. Держи правила в синхроне со
// schema/assistant-result.schema.json — оба описывают один и тот же контракт.
func DecodeEnvelope(raw []byte) (Envelope, error) {
	var disc discriminator
	if err := json.Unmarshal(raw, &disc); err != nil {
		return Envelope{}, fmt.Errorf("parse assistant result JSON: %w", err)
	}

	switch disc.Kind {
	case KindResult:
		return decodeResultEnvelope(raw)
	case KindDelegate:
		return decodeDelegateEnvelope(raw)
	case "":
		return Envelope{}, errors.New(`assistant result is missing required discriminator "kind"`)
	default:
		return Envelope{}, fmt.Errorf("assistant result has unknown kind %q", disc.Kind)
	}
}

func decodeResultEnvelope(raw []byte) (Envelope, error) {
	var e resultEnvelope
	if err := strictDecode(raw, &e); err != nil {
		return Envelope{}, err
	}
	if e.SchemaVersion != SupportedSchemaVersion {
		return Envelope{}, fmt.Errorf("unsupported schema_version %d (expected %d)", e.SchemaVersion, SupportedSchemaVersion)
	}
	switch e.Verdict {
	case VerdictPass, VerdictNeedsChanges, VerdictInconclusive:
	default:
		return Envelope{}, fmt.Errorf("invalid verdict value %q", e.Verdict)
	}
	if err := validateVerdictConsistency(e.Verdict, e.Findings); err != nil {
		return Envelope{}, err
	}

	result := &Result{
		Verdict:         e.Verdict,
		Summary:         e.Summary,
		Findings:        e.Findings,
		Evidence:        e.Evidence,
		ContextVersion:  ContextVersion(e.ContextVersion),
		SuggestedChange: e.SuggestedChange,
		Limitations:     e.Limitations,
	}
	return Envelope{SchemaVersion: e.SchemaVersion, Kind: KindResult, Result: result}, nil
}

func decodeDelegateEnvelope(raw []byte) (Envelope, error) {
	var e delegateEnvelope
	if err := strictDecode(raw, &e); err != nil {
		return Envelope{}, err
	}
	if e.SchemaVersion != SupportedSchemaVersion {
		return Envelope{}, fmt.Errorf("unsupported schema_version %d (expected %d)", e.SchemaVersion, SupportedSchemaVersion)
	}
	if len(e.Children) == 0 {
		return Envelope{}, errors.New(`assistant result with kind:"delegate" requires at least one child in "children"`)
	}

	delegate := &Delegate{Summary: e.Summary, Children: e.Children, Continuation: e.Continuation}
	return Envelope{SchemaVersion: e.SchemaVersion, Kind: KindDelegate, Delegate: delegate}, nil
}

// strictDecode разбирает ОДИН JSON-документ без неизвестных полей и без
// хвостового контента после него (например, лишнего текста или второго
// документа).
func strictDecode(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("parse assistant result JSON: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing content after JSON document in assistant result")
	}
	return nil
}

// validateVerdictConsistency — verdict:"pass" не может сосуществовать с
// находкой severity:"blocker": по определению blocker обязан переводить ход в
// needs_changes.
func validateVerdictConsistency(v Verdict, findings []Finding) error {
	if v != VerdictPass {
		return nil
	}
	for _, f := range findings {
		if f.Severity == "blocker" {
			return errors.New(`verdict:"pass" cannot coexist with a finding of severity:"blocker"`)
		}
	}
	return nil
}
