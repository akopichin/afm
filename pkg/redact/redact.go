// Package redact содержит нейтральный потоковый редактор секретов: io.Writer и строковый хелпер,
// заменяющие вхождения известных секретных значений на безопасный маркер.
package redact

import (
	"bytes"
	"io"
	"slices"
	"strings"
)

// DefaultMarker — маркер по умолчанию; экспортируется как константа, а не повторяющийся литерал, и
// тесты/потребители ссылаются на неё же вместо копий строки.
const DefaultMarker = "[REDACTED]"

// Writer — потоковый редактор секретов: io.Writer, заменяющий вхождения известных секретных значений на
// безопасный маркер перед записью в нижележащий writer.
type Writer struct {
	w       io.Writer
	secrets []string // дедуп, отсортированы по убыванию длины
	marker  string   // безопасен: не содержит секрета, ни один его суффикс не префикс секрета, и сам не подстрока секрета
	maxLen  int      // длина самого длинного секрета
	buf     []byte   // СЫРОЙ хвост ≤ maxLen-1 байт (возможное начало секрета на границе Write)
}

// NewWriter оборачивает w редактором, маскирующим переданные secrets. Пустые и дублирующиеся значения
// отбрасываются; маркер выбирается безопасным (см. marker).
func NewWriter(w io.Writer, secrets []string) *Writer {
	seen := map[string]bool{}
	nonEmpty := make([]string, 0, len(secrets))
	longest := 0
	for _, s := range secrets {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		nonEmpty = append(nonEmpty, s)
		if len(s) > longest {
			longest = len(s)
		}
	}
	slices.SortFunc(nonEmpty, func(a, b string) int { return len(b) - len(a) })
	return &Writer{w: w, secrets: nonEmpty, marker: marker(nonEmpty), maxLen: longest}
}

// Write — bounded streaming-редакция без границы по строкам и без риска утечки: 1. Память ограничена
// maxLen-1 + len(p): buf после Write ≤ maxLen-1.
func (r *Writer) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	red := all(r.buf, r.secrets, r.marker)
	hold := r.longestSecretPrefixSuffix(red)
	flush := len(red) - hold
	if flush > 0 {
		if _, err := r.w.Write(red[:flush]); err != nil {
			return 0, err
		}
	}
	r.buf = append(r.buf[:0], red[flush:]...) // хвост = сырой частичный префикс
	return len(p), nil
}

// longestSecretPrefixSuffix — длина самого длинного суффикса b, равного
// собственному префиксу какого-либо секрета (≤ maxLen-1).
func (r *Writer) longestSecretPrefixSuffix(b []byte) int {
	best := 0
	for _, s := range r.secrets {
		lim := len(s) - 1
		if lim > len(b) {
			lim = len(b)
		}
		for k := lim; k > best; k-- {
			if bytes.HasPrefix([]byte(s), b[len(b)-k:]) {
				best = k
				break
			}
		}
	}
	return best
}

// Close сбрасывает удержанный хвост, отредактировав его целиком (на этом этапе
// достроиться до секрета он уже не может — новых Write не будет).
func (r *Writer) Close() error {
	if len(r.buf) > 0 {
		if _, err := r.w.Write(all(r.buf, r.secrets, r.marker)); err != nil {
			return err
		}
		r.buf = nil
	}
	return nil
}

// marker исключает секреты, их префиксы на своих суффиксах и вхождение в секрет; без безопасного маркера
// возвращает пустую строку.
func marker(secrets []string) string {
	for _, cand := range []string{DefaultMarker, "[[hook-secret-redacted]]", "\x00REDACTED\x00"} {
		if !containsAnySecret(cand, secrets) && !anySuffixIsSecretPrefix(cand, secrets) && !anySecretContains(cand, secrets) {
			return cand
		}
	}
	return ""
}

func containsAnySecret(s string, secrets []string) bool {
	for _, sec := range secrets {
		if sec != "" && strings.Contains(s, sec) {
			return true
		}
	}
	return false
}

// anySuffixIsSecretPrefix — есть ли непустой суффикс s, являющийся префиксом
// какого-либо секрета.
func anySuffixIsSecretPrefix(s string, secrets []string) bool {
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		for k := 1; k <= len(s) && k <= len(sec); k++ {
			if strings.HasPrefix(sec, s[len(s)-k:]) {
				return true
			}
		}
	}
	return false
}

// anySecretContains — встречается ли mark как подстрока в каком-либо секрете.
func anySecretContains(mark string, secrets []string) bool {
	for _, sec := range secrets {
		if sec != "" && strings.Contains(sec, mark) {
			return true
		}
	}
	return false
}

// all заменяет все вхождения секретов на mark ДО фиксированной точки: замена одного секрета может
// СИНТЕЗИРОВАТЬ другой из окружающего контекста.
func all(b []byte, secrets []string, mark string) []byte {
	maxPasses := len(b) + 1 // зафиксировать ДО цикла: при fallback-маркере "" строка
	// сжимается, а пересчёт len(b) в условии дал бы ранний выход с остатком секрета (codex).
	for pass := 0; pass < maxPasses; pass++ {
		changed := false
		for _, s := range secrets {
			if s == "" {
				continue
			}
			if bytes.Contains(b, []byte(s)) {
				b = bytes.ReplaceAll(b, []byte(s), []byte(mark))
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return b
}

// String редактирует все вхождения secrets в s. Пустой список секретов —
// no-op (строка возвращается без изменений).
func String(s string, secrets []string) string {
	if len(secrets) == 0 {
		return s
	}
	return string(all([]byte(s), secrets, marker(secrets)))
}
