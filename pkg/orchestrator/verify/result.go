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
	// VerdictAcceptedStalemate синтезируется САМИМ afm (не декодируется из
	// ответа модели) при срабатывании review_patience: стадия принята как
	// завершённая, потому что автор перестал менять дерево в ответ на
	// повторные verify-отклонения (reviewer/author stalemate). Намеренно НЕ
	// входит в allow-list DecodeModelResult ниже — модель не вправе его
	// вернуть; значение живёт только на пути отображения (verify-result
	// нотис → latestVerifyByStage/feed-view-model).
	VerdictAcceptedStalemate Verdict = "accepted_stalemate"
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
	// Markdown-fence отклоняем до любого ремонта: это намеренная строгость (модель
	// обязана отдавать голый JSON), а jsonrepair умеет снимать fence и тем самым
	// обошёл бы это правило.
	if bytes.HasPrefix(trimmed, []byte("```")) {
		return ModelResult{}, errors.New("verification result is wrapped in a markdown code fence; expected bare JSON")
	}

	r, err := decodeStrict(trimmed)
	if err == nil {
		return r, nil
	}

	// Ремонтируем ТОЛЬКО одну узкую поломку: сырые управляющие символы
	// (перенос строки/возврат каретки/таб) ВНУТРИ строкового литерала JSON —
	// именно это выдают ревьюеры, вписывая многострочную цитату кода в поле
	// evidence. escapeRawControlCharsInStrings НЕ снимает fence/JSONP/
	// комментарии и не отбрасывает лишний контент (в отличие от jsonrepair),
	// поэтому все намеренные строгие отказы сохраняются: после ремонта мы
	// повторно прогоняем ВЕСЬ строгий конвейер, и дубли ключей, неизвестные
	// поля, trailing-контент, неверные schema_version/verdict по-прежнему
	// отвергаются. Применяем ремонт, только если вход вообще содержал сырой
	// управляющий символ (иначе чинить нечего — возвращаем исходную ошибку).
	if repaired, changed := escapeRawControlCharsInStrings(trimmed); changed {
		if r2, err2 := decodeStrict(repaired); err2 == nil {
			return r2, nil
		}
	}
	return ModelResult{}, err
}

// escapeRawControlCharsInStrings проходит JSON побайтово, отслеживая, находимся
// ли мы внутри строкового литерала, и заменяет сырые управляющие символы внутри
// строк на их валидные escape-последовательности (\n, \r, \t; прочие < 0x20 —
// на \u00XX). Вне строк байты не трогаются, поэтому структура документа
// (границы, лишний контент, fence) остаётся как есть — это намеренно узкий
// ремонт, не нормализатор. Возвращает (возможно изменённые) байты и флаг, были
// ли замены.
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
// Порядок важен: структурный разбор (json.Decode) идёт ПЕРВЫМ, поэтому
// лексическая ошибка выходит как "failed to parse …" с развёрнутым
// *json.SyntaxError (а не маскируется под "duplicate key"), и вызывающий может
// отличить её по типу, чтобы решить, стоит ли пробовать jsonrepair. Проверка
// дубликатов ключей идёт ПОСЛЕ успешного разбора (json сам дубли молча
// схлопывает, поэтому их всё равно надо ловить отдельным проходом).
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
