// Package verify описывает контракт машинного результата AI-verify — строгий декодер финального
// JSON-ответа верификатора — и тип, отделяющий успех/неуспех запуска процесса от вердикта модели.
package verify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
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
	SchemaVersion int       `json:"schema_version"`
	Verdict       Verdict   `json:"verdict"`
	Summary       string    `json:"summary"`
	Findings      []Finding `json:"findings"`
}

// MaxResultBytes — верхняя граница размера сырого JSON-ответа.
const MaxResultBytes = 256 * 1024

// SupportedSchemaVersion — единственная поддерживаемая версия схемы результата.
const SupportedSchemaVersion = 1

// DecodeModelResult строго разбирает сырой JSON-ответ верификатора и проверяет его внутреннюю
// согласованность.
func DecodeModelResult(raw []byte) (ModelResult, error) {
	if len(raw) > MaxResultBytes {
		return ModelResult{}, fmt.Errorf("verification result exceeds %d byte limit (got %d)", MaxResultBytes, len(raw))
	}

	trimmed := bytes.TrimSpace(raw)
	// Markdown-fence отклоняем до любого ремонта: это намеренная строгость (модель обязана отдавать голый
	// JSON), а jsonrepair умеет снимать fence и тем самым обошёл бы это правило.
	if bytes.HasPrefix(trimmed, []byte("```")) {
		return ModelResult{}, errors.New("verification result is wrapped in a markdown code fence; expected bare JSON")
	}

	r, err := decodeStrict(trimmed)
	if err == nil {
		return r, nil
	}

	// Ремонтируем ТОЛЬКО одну узкую поломку: сырые управляющие символы ВНУТРИ строкового литерала JSON —
	// именно это выдают ревьюеры, вписывая многострочную цитату кода в поле evidence.
	if repaired, changed := escapeRawControlCharsInStrings(trimmed); changed {
		if r2, err2 := decodeStrict(repaired); err2 == nil {
			return r2, nil
		}
	}
	return ModelResult{}, err
}

// escapeRawControlCharsInStrings экранирует сырые control-байты только внутри JSON-строк; возвращает байты
// и признак замены.
func escapeRawControlCharsInStrings(data []byte) ([]byte, bool) {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(data)+8)
	inString := false
	escaped := false
	changed := false
	for _, b := range data {
		if inString {
			if escaped { // предыдущий байт был '\' — текущий уже экранирован
				out = append(out, b)
				escaped = false
				continue
			}
			switch {
			case b == '\\':
				escaped = true
			case b == '"':
				inString = false
			case b < 0x20: // сырой управляющий символ внутри строки недопустим — экранируем
				switch b {
				case '\n':
					out = append(out, '\\', 'n')
				case '\r':
					out = append(out, '\\', 'r')
				case '\t':
					out = append(out, '\\', 't')
				default:
					out = append(out, '\\', 'u', '0', '0', hexdigits[b>>4], hexdigits[b&0xf])
				}
				changed = true
				continue
			default:
				// обычный байт строки — добавляется ниже без изменений
			}
			out = append(out, b)
			continue
		}
		if b == '"' {
			inString = true
		}
		out = append(out, b)
	}
	if !changed {
		return data, false
	}
	return out, true
}

// decodeStrict — строгий конвейер разбора ОДНОГО JSON-документа без репейра.
func decodeStrict(trimmed []byte) (ModelResult, error) {
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var r ModelResult
	if err := dec.Decode(&r); err != nil {
		return ModelResult{}, fmt.Errorf("failed to parse verification result JSON: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return ModelResult{}, errors.New("trailing content after JSON document in verification result (extra data or multiple documents)")
	}

	if err := detectDuplicateKeys(trimmed); err != nil {
		return ModelResult{}, fmt.Errorf("duplicate key in verification result JSON: %w", err)
	}

	if r.SchemaVersion != SupportedSchemaVersion {
		return ModelResult{}, fmt.Errorf("unsupported schema_version %d (expected %d)", r.SchemaVersion, SupportedSchemaVersion)
	}

	switch r.Verdict {
	case VerdictPass, VerdictNeedsChanges, VerdictInconclusive:
	default:
		return ModelResult{}, fmt.Errorf("invalid verdict value %q", r.Verdict)
	}

	if err := validateVerdictConsistency(r); err != nil {
		return ModelResult{}, err
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

// detectDuplicateKeys проходит JSON токен за токеном и ищет повторяющиеся ключи внутри одного объекта на
// любой глубине вложенности.
func detectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	return checkDuplicateValue(dec)
}

// checkDuplicateValue разбирает одно JSON-значение (объект, массив или
// скаляр), рекурсивно проверяя дубликаты ключей в каждом вложенном объекте.
func checkDuplicateValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // скаляр (строка/число/bool/null) — дубликатов ключей внутри нет
	}

	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := keyTok.(string)
			if _, dup := seen[key]; dup {
				return fmt.Errorf("key %q appears more than once in the same object", key)
			}
			seen[key] = struct{}{}
			if err := checkDuplicateValue(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // закрывающая '}'
		return err
	case '[':
		for dec.More() {
			if err := checkDuplicateValue(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // закрывающая ']'
		return err
	}
	return nil
}
