package assistants

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/jsonstrict"
	"github.com/akopichin/afm/schema"
)

// MaxResultBytes — верхняя граница размера сырого JSON-ответа помощника, проверяется на ПОЛНОМ входе до
// любого парсинга (jsonstrict.EnforceMaxBytes) — то же правило и тот же порядок, что у
// verify.MaxResultBytes.
const MaxResultBytes = 256 * 1024

// SupportedSchemaVersion — единственная поддерживаемая версия envelope-схемы
// ответа помощника (schema/assistant-result.schema.json). Держи в синхроне с
// "const": 1 в схеме.
const SupportedSchemaVersion = 1

// MaxDelegateChildren — верхняя граница числа детей в одном kind:"delegate" ответе (T4a.1). Никакого
// явного числа в спеке нет; 8 — консервативный предел одного delegation-хода (держит admission-группу
// (T4a.2) маленькой и проверяемой атомарно), держи в синхроне с schema/assistant-result.schema.json's
// delegateChild maxItems.
const MaxDelegateChildren = 8

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

// ClosesRequiredGate reports whether this envelope represents a turn that can close a required-mode gate
// (BlocksRootCompletion, tree.go): only kind:"result" with verdict:"pass" can. kind:"delegate" never does —
// it has no verdict field at all, so there is no "did the model actually mean pass" text to misread; the
// orchestrator must call this instead of re-deriving the same check inline wherever it decides whether a
// turn's outcome is eligible to satisfy a required dependent.
func (e Envelope) ClosesRequiredGate() bool {
	return e.Kind == KindResult && e.Result != nil && e.Result.Verdict.ClosesGate()
}

// resultEnvelope — JSON-форма kind:"result" (зеркало definitions.result схемы). Свободный текст (summary,
// evidence, suggested_change, limitations) принимается и строкой, и списком строк (freetext.go).
type resultEnvelope struct {
	SchemaVersion   jsonstrict.Integer `json:"schema_version"`
	Kind            Kind               `json:"kind"`
	Verdict         Verdict            `json:"verdict"`
	Summary         textField          `json:"summary"`
	Findings        []Finding          `json:"findings,omitempty"`
	Evidence        textListField      `json:"evidence,omitempty"`
	ContextVersion  string             `json:"context_version,omitempty"`
	SuggestedChange textField          `json:"suggested_change,omitempty"`
	Limitations     textField          `json:"limitations,omitempty"`
}

// delegateEnvelope — JSON-форма kind:"delegate" (зеркало definitions.delegate схемы).
type delegateEnvelope struct {
	SchemaVersion jsonstrict.Integer `json:"schema_version"`
	Kind          Kind               `json:"kind"`
	Summary       textField          `json:"summary,omitempty"`
	Children      []DelegateRequest  `json:"children"`
	Continuation  *Continuation      `json:"continuation,omitempty"`
}

// discriminator — первый проход разбора: достаточно только kind, чтобы решить,
// какой из двух envelope-типов разбирать строго.
type discriminator struct {
	Kind Kind `json:"kind"`
}

// DecodeResult строго разбирает сырой JSON-ответ помощника через общие транспортные примитивы
// pkg/jsonstrict (кап размера — на ПОЛНОМ входе до парсинга, markdown-fence, дубли ключей на любой глубине,
// хвостовой контент, узкий ремонт сырых переводов строк), поверх которых лежит собственная схема этого
// пакета: обязательный дискриминатор kind, неподдерживаемая schema_version и согласованность
// verdict/findings. Держи правила в синхроне со schema/assistant-result.schema.json — оба описывают один и
// тот же контракт.
func DecodeResult(raw []byte) (Envelope, error) {
	if err := jsonstrict.EnforceMaxBytes(raw, MaxResultBytes); err != nil {
		return Envelope{}, fmt.Errorf("assistant %w", err)
	}

	trimmed := bytes.TrimSpace(raw)
	if err := jsonstrict.RejectFence(trimmed); err != nil {
		return Envelope{}, fmt.Errorf("assistant %w", err)
	}

	env, err := decodeEnvelopeStrict(trimmed)
	if err == nil {
		return env, nil
	}

	// Тот же узкий ремонт, что verify.DecodeModelResult: сырые управляющие символы ВНУТРИ строкового
	// литерала (например, многострочная цитата кода в summary/detail без экранирования).
	if repaired, changed := jsonstrict.RepairRawNewlines(trimmed); changed && jsonstrict.EnforceMaxBytes(repaired, MaxResultBytes) == nil {
		if env2, err2 := decodeEnvelopeStrict(repaired); err2 == nil {
			return env2, nil
		}
	}
	return Envelope{}, err
}

// DecodeEnvelope — back-compat alias of DecodeResult (T3.2 extraction); existing callers/tests keep this
// name.
func DecodeEnvelope(raw []byte) (Envelope, error) {
	return DecodeResult(raw)
}

// decodeEnvelopeStrict разбирает уже прошедший размер/fence-проверки документ: дискриминатор kind, дубли
// ключей на любой глубине, затем доменная схема конкретного kind.
func decodeEnvelopeStrict(raw []byte) (Envelope, error) {
	if err := jsonstrict.DecodeNoDuplicates(raw); err != nil {
		return Envelope{}, fmt.Errorf("parse assistant result JSON: %w", err)
	}
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
		return Envelope{}, jsonstrict.NewIssue(jsonstrict.DomainIssue, errors.New(`assistant result is missing required discriminator "kind"`))
	default:
		return Envelope{}, jsonstrict.NewIssue(jsonstrict.DomainIssue, fmt.Errorf("assistant result has unknown kind %q", disc.Kind))
	}
}

func decodeResultEnvelope(raw []byte) (Envelope, error) {
	var e resultEnvelope
	if err := strictDecode(raw, &e); err != nil {
		return Envelope{}, err
	}
	if e.SchemaVersion != SupportedSchemaVersion {
		return Envelope{}, jsonstrict.NewIssue(jsonstrict.DomainIssue, fmt.Errorf("unsupported schema_version %d (expected %d)", e.SchemaVersion, SupportedSchemaVersion))
	}
	switch e.Verdict {
	case VerdictPass, VerdictNeedsChanges, VerdictInconclusive:
	default:
		return Envelope{}, jsonstrict.NewIssue(jsonstrict.DomainIssue, fmt.Errorf("invalid verdict value %q", e.Verdict))
	}
	if err := validateVerdictConsistency(e.Verdict, e.Findings); err != nil {
		return Envelope{}, jsonstrict.NewIssue(jsonstrict.DomainIssue, err)
	}
	if err := schema.Validate(schema.AssistantResult, raw); err != nil {
		return Envelope{}, jsonstrict.NewIssue(jsonstrict.StructureIssue, err)
	}

	result := &Result{
		Verdict:         e.Verdict,
		Summary:         string(e.Summary),
		Findings:        e.Findings,
		Evidence:        e.Evidence,
		ContextVersion:  ContextVersion(e.ContextVersion),
		SuggestedChange: string(e.SuggestedChange),
		Limitations:     string(e.Limitations),
	}
	return Envelope{SchemaVersion: int(e.SchemaVersion), Kind: KindResult, Result: result}, nil
}

func decodeDelegateEnvelope(raw []byte) (Envelope, error) {
	var e delegateEnvelope
	if err := strictDecode(raw, &e); err != nil {
		return Envelope{}, err
	}
	if e.SchemaVersion != SupportedSchemaVersion {
		return Envelope{}, jsonstrict.NewIssue(jsonstrict.DomainIssue, fmt.Errorf("unsupported schema_version %d (expected %d)", e.SchemaVersion, SupportedSchemaVersion))
	}
	if len(e.Children) == 0 {
		return Envelope{}, errors.New(`assistant result with kind:"delegate" requires at least one child in "children"`)
	}
	if len(e.Children) > MaxDelegateChildren {
		return Envelope{}, fmt.Errorf("assistant result with kind:\"delegate\" requests %d children, exceeding the cap of %d", len(e.Children), MaxDelegateChildren)
	}

	if err := schema.Validate(schema.AssistantResult, raw); err != nil {
		return Envelope{}, jsonstrict.NewIssue(jsonstrict.StructureIssue, err)
	}
	delegate := &Delegate{Summary: string(e.Summary), Children: e.Children, Continuation: e.Continuation}
	return Envelope{SchemaVersion: int(e.SchemaVersion), Kind: KindDelegate, Delegate: delegate}, nil
}

// strictDecode разбирает ОДИН JSON-документ без неизвестных полей и без
// хвостового контента после него (например, лишнего текста или второго
// документа) — тонкая обёртка над jsonstrict.DecodeDisallowUnknown.
func strictDecode(raw []byte, v any) error {
	if err := jsonstrict.DecodeDisallowUnknown(raw, v); err != nil {
		if errors.Is(err, jsonstrict.ErrTrailingContent) {
			return fmt.Errorf("%w in assistant result", err)
		}
		return fmt.Errorf("parse assistant result JSON: %w", err)
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
