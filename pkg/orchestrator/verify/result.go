// Package verify описывает контракт машинного результата AI-verify —
// строгий декодер финального JSON-ответа верификатора — и тип, отделяющий
// успех/неуспех запуска процесса от вердикта модели. Пакет чистый: без
// подпроцессов и без оркестрации, полностью юнит-тестируемый.
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
)

// Finding — одно замечание верификатора. Path/LineStart/LineEnd — указатели,
// потому что замечание может не быть привязано к конкретному файлу/строкам
// (null в JSON). Id и метаданные запуска (run/stage/verification-id/время)
// присваивает AFM, а не модель — их в контракте намеренно нет: если модель
// всё же пришлёт такое поле, DisallowUnknownFields отклонит его как
// неизвестное.
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

// MaxResultBytes — верхняя граница размера сырого JSON-ответа. Проверяется
// ДО разбора: усечённый по лимиту префикс мог бы случайно оказаться валидным
// JSON и пройти как "тихо обрезанный", поэтому решение принимается по полной
// длине входа, не по префиксу.
const MaxResultBytes = 256 * 1024

// SupportedSchemaVersion — единственная поддерживаемая версия схемы
// результата. Любое другое значение отклоняется явной ошибкой, а не молча
// игнорируется — совместимость обеспечивает будущая миграция схемы, не
// декодер.
const SupportedSchemaVersion = 1

// DecodeModelResult строго разбирает сырой JSON-ответ верификатора и
// проверяет его внутреннюю согласованность (версия схемы, допустимость
// вердикта, соответствие findings вердикту). Возвращает ошибку, называющую
// конкретную причину отказа (текст ошибки — на английском, по конвенции
// пакета, см. pkg/orchestrator/stagefiles/completion.go).
func DecodeModelResult(raw []byte) (ModelResult, error) {
	if len(raw) > MaxResultBytes {
		return ModelResult{}, fmt.Errorf("verification result exceeds %d byte limit (got %d)", MaxResultBytes, len(raw))
	}

	trimmed := bytes.TrimSpace(raw)
	if bytes.HasPrefix(trimmed, []byte("```")) {
		return ModelResult{}, errors.New("verification result is wrapped in a markdown code fence; expected bare JSON")
	}

	if err := detectDuplicateKeys(trimmed); err != nil {
		return ModelResult{}, fmt.Errorf("duplicate key in verification result JSON: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var r ModelResult
	if err := dec.Decode(&r); err != nil {
		return ModelResult{}, fmt.Errorf("failed to parse verification result JSON: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return ModelResult{}, errors.New("trailing content after JSON document in verification result (extra data or multiple documents)")
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

// validateVerdictConsistency проверяет, что вердикт согласован с findings и
// summary: pass не может нести блокирующие находки, needs_changes обязан
// нести хотя бы одну, а summary всегда обязателен (для inconclusive он же
// объясняет причину невозможности проверки).
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

// detectDuplicateKeys проходит JSON токен за токеном и ищет повторяющиеся
// ключи внутри одного объекта на любой глубине вложенности. json.Decoder
// сам по себе такие дубликаты не ловит (при декодировании в struct/map
// последнее значение молча побеждает) — это единственный способ отклонить
// их явной ошибкой, а не проглотить.
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
