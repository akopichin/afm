// Package verify описывает контракт машинного результата AI-verify — строгий декодер финального
// JSON-ответа верификатора — и тип, отделяющий успех/неуспех запуска процесса от вердикта модели.
package verify

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/akopichin/afm/pkg/jsonstrict"
	"github.com/akopichin/afm/schema"
)

// Verdict — итоговое решение верификатора по стадии.
type Verdict string

const (
	VerdictPass         Verdict = "pass"
	VerdictNeedsChanges Verdict = "needs_changes"
	VerdictInconclusive Verdict = "inconclusive"
	// VerdictAcceptedStalemate создаёт только afm при review_patience; модель не вправе возвращать этот
	// вердикт.
	VerdictAcceptedStalemate Verdict = "accepted_stalemate"
)

// Finding — одно замечание верификатора. Path/LineStart/LineEnd — указатели, потому что замечание может не
// быть привязано к конкретному файлу/строкам (null в JSON).
type Finding struct {
	Blocking    bool    `json:"blocking"`
	Title       string  `json:"title"`
	Path        *string `json:"path"`
	LineStart   *int    `json:"line_start"`
	LineEnd     *int    `json:"line_end"`
	Requirement string  `json:"requirement"`
	Evidence    string  `json:"evidence"`
	MinimalFix  string  `json:"minimal_fix"`
}

// ModelResult — финальный JSON-ответ верификатора в разобранном виде.
type ModelResult struct {
	SchemaVersion jsonstrict.Integer `json:"schema_version"`
	Verdict       Verdict            `json:"verdict"`
	Summary       string             `json:"summary"`
	Findings      []Finding          `json:"findings"`
}

// MaxResultBytes — верхняя граница размера сырого JSON-ответа.
const MaxResultBytes = 256 * 1024

// SupportedSchemaVersion — единственная поддерживаемая версия схемы результата.
const SupportedSchemaVersion = 1

// DecodeModelResult строго разбирает сырой JSON-ответ верификатора и проверяет его внутреннюю
// согласованность.
func DecodeModelResult(raw []byte) (ModelResult, error) {
	if err := jsonstrict.EnforceMaxBytes(raw, MaxResultBytes); err != nil {
		return ModelResult{}, fmt.Errorf("verification %w", err)
	}

	trimmed := bytes.TrimSpace(raw)
	// Markdown-fence отклоняем до любого ремонта: это намеренная строгость (модель обязана отдавать голый
	// JSON), а jsonrepair умеет снимать fence и тем самым обошёл бы это правило.
	if err := jsonstrict.RejectFence(trimmed); err != nil {
		return ModelResult{}, fmt.Errorf("verification %w", err)
	}

	r, err := decodeStrict(trimmed)
	if err == nil {
		return r, nil
	}

	// Ремонтируем ТОЛЬКО одну узкую поломку: сырые управляющие символы ВНУТРИ строкового литерала JSON —
	// именно это выдают ревьюеры, вписывая многострочную цитату кода в поле evidence.
	if repaired, changed := jsonstrict.RepairRawNewlines(trimmed); changed && jsonstrict.EnforceMaxBytes(repaired, MaxResultBytes) == nil {
		if r2, err2 := decodeStrict(repaired); err2 == nil {
			return r2, nil
		}
	}
	return ModelResult{}, err
}

// RecoverModelResult exposes accepted candidate bytes while retaining verify's narrow repair policy.
func RecoverModelResult(raw []byte) (ModelResult, []byte, jsonstrict.RepairKind, error) {
	r, err := DecodeModelResult(raw)
	if err == nil {
		if fixed, changed := jsonstrict.RepairRawNewlines(raw); changed {
			return r, fixed, jsonstrict.MechanicalRepair, nil
		}
		return r, raw, "", nil
	}
	if len(raw) > MaxResultBytes {
		return ModelResult{}, nil, "", err
	}
	if _, complete := jsonstrict.SingleObject(raw); !complete {
		err = jsonstrict.NewIssue(jsonstrict.RefusalIssue, err)
	}
	return ModelResult{}, nil, "", err
}

// decodeStrict — строгий конвейер разбора ОДНОГО JSON-документа без репейра.
func decodeStrict(trimmed []byte) (ModelResult, error) {
	if err := jsonstrict.DecodeNoDuplicates(trimmed); err != nil {
		return ModelResult{}, fmt.Errorf("parse verification result JSON: %w", err)
	}
	var r ModelResult
	if err := jsonstrict.DecodeDisallowUnknown(trimmed, &r); err != nil {
		if errors.Is(err, jsonstrict.ErrTrailingContent) {
			return ModelResult{}, fmt.Errorf("%w in verification result (extra data or multiple documents)", err)
		}
		return ModelResult{}, fmt.Errorf("failed to parse verification result JSON: %w", err)
	}

	if r.SchemaVersion != SupportedSchemaVersion {
		return ModelResult{}, jsonstrict.NewIssue(jsonstrict.DomainIssue, fmt.Errorf("unsupported schema_version %d (expected %d)", r.SchemaVersion, SupportedSchemaVersion))
	}

	switch r.Verdict {
	case VerdictPass, VerdictNeedsChanges, VerdictInconclusive:
	default:
		return ModelResult{}, jsonstrict.NewIssue(jsonstrict.DomainIssue, fmt.Errorf("invalid verdict value %q", r.Verdict))
	}

	if err := validateVerdictConsistency(r); err != nil {
		return ModelResult{}, jsonstrict.NewIssue(jsonstrict.DomainIssue, err)
	}
	if err := schema.Validate(schema.VerifyResult, trimmed); err != nil {
		return ModelResult{}, jsonstrict.NewIssue(jsonstrict.StructureIssue, err)
	}

	return r, nil
}

// validateVerdictConsistency требует summary; pass запрещает blocking findings, needs_changes требует хотя
// бы один.
func validateVerdictConsistency(r ModelResult) error {
	blocking := 0
	for _, f := range r.Findings {
		if f.Blocking {
			blocking++
		}
	}

	switch r.Verdict {
	case VerdictPass:
		if blocking > 0 {
			return errors.New("verdict=pass cannot have any blocking findings")
		}
	case VerdictNeedsChanges:
		if blocking == 0 {
			return errors.New("verdict=needs_changes requires at least one blocking finding")
		}
	case VerdictInconclusive:
		if strings.TrimSpace(r.Summary) == "" {
			return errors.New("summary is required for verdict=inconclusive to explain why")
		}
	default:
		// недостижимо: вердикт уже проверен в DecodeModelResult до вызова этой функции.
	}

	if r.Verdict != VerdictInconclusive && strings.TrimSpace(r.Summary) == "" {
		return fmt.Errorf("summary is required for verdict=%s", r.Verdict)
	}

	return nil
}

// escapeRawControlCharsInStrings is a thin back-compat forward to jsonstrict.RepairRawNewlines (T3.2
// extracted the actual implementation into pkg/jsonstrict, shared with pkg/assistants' decoder) — kept so
// this package's own white-box test of the narrow repair keeps working unmodified.
func escapeRawControlCharsInStrings(data []byte) ([]byte, bool) {
	return jsonstrict.RepairRawNewlines(data)
}
