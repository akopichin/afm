package lifecyclehooks

import (
	"bytes"
	"io"
	"slices"
	"strings"
)

// defaultRedactionMarker — маркер по умолчанию (пробуется первым в
// redactMarker); экспортируется как константа, а не повторяющийся литерал
// (goconst), и тесты пакета ссылаются на неё же вместо копий строки.
const defaultRedactionMarker = "[REDACTED]"

// redactingWriter — потоковый редактор секретов: io.Writer, заменяющий
// вхождения известных секретных значений на безопасный маркер перед записью
// в нижележащий writer. Bounded-память: buf удерживает ≤ maxLen-1 байт
// сырого хвоста (возможное начало секрета на границе Write).
type redactingWriter struct {
	w       io.Writer
	secrets []string // дедуп, отсортированы по убыванию длины
	marker  string   // безопасен: не содержит секрета, ни один его суффикс не префикс секрета, и сам не подстрока секрета
	maxLen  int      // длина самого длинного секрета
	buf     []byte   // СЫРОЙ хвост ≤ maxLen-1 байт (возможное начало секрета на границе Write)
}

func newRedactingWriter(w io.Writer, secrets []string) *redactingWriter {
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
	return &redactingWriter{w: w, secrets: nonEmpty, marker: redactMarker(nonEmpty), maxLen: longest}
}

// Write — bounded streaming-редакция без границы по строкам и без риска утечки:
//  1. buf += p; red := redactAll(buf) — все ПОЛНЫЕ вхождения заменены на marker.
//  2. hold := longest суффикс red, являющийся собственным префиксом какого-либо
//     секрета (≤ maxLen-1) — единственное, что может достроиться до секрета
//     следующим Write. Т.к. marker подобран так, что НИ ОДИН его суффикс не
//     является префиксом секрета (redactMarker), hold никогда не попадает на
//     байты marker — удержанные байты идентичны сырым (частичный префикс).
//  3. Сбрасываем red[:len-hold], оставляем red[len-hold:] как новый buf.
//
// Память ограничена maxLen-1 + len(p): buf после Write ≤ maxLen-1. Секрет,
// разорванный между Write, достраивается и заменяется; полный секрет в одном
// Write заменяется сразу. Утечки на границе нет (hold покрывает любой возможный
// недостроенный префикс).
func (r *redactingWriter) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	red := redactAll(r.buf, r.secrets, r.marker)
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
func (r *redactingWriter) longestSecretPrefixSuffix(b []byte) int {
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

func (r *redactingWriter) Close() error {
	if len(r.buf) > 0 {
		if _, err := r.w.Write(redactAll(r.buf, r.secrets, r.marker)); err != nil {
			return err
		}
		r.buf = nil
	}
	return nil
}

// redactMarker выбирает маркер, который безопасен по трём независимым условиям:
//
//	(а) не содержит ни одного секрета целиком;
//	(б) ни один его непустой суффикс не является префиксом какого-либо секрета —
//	    гарантирует, что удержание хвоста в Write никогда не заденет байты
//	    маркера (иначе секрет вида "]x" при маркере, оканчивающемся на "]",
//	    ломал бы лог — codex);
//	(в) сам маркер не встречается как подстрока ни в одном секрете — иначе
//	    редактирование ДРУГОГО секрета рядом с уже сброшенным контекстом может
//	    ВОССТАНОВИТЬ этот секрет: секреты "QR" и "a[REDACTED]b", запись
//	    двумя Write "aQ"+"Rb" → флаш "a", затем "QR"→маркер даёт в логе
//	    "a[REDACTED]b" — байт-в-байт значение второго секрета (codex).
//
// Fallback — "" (удаление), если безопасного маркера нет: пустая строка сама
// по себе ничего не добавляет в поток, так что условие (в) для неё выполнено
// тривиально (подстрокой секрета быть не может — она пустая).
func redactMarker(secrets []string) string {
	for _, cand := range []string{defaultRedactionMarker, "[[hook-secret-redacted]]", "\x00REDACTED\x00"} {
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

// anySecretContains — встречается ли marker как подстрока в каком-либо
// секрете. Условие (в) redactMarker: если да, подстановка этого маркера
// рядом с окружающим контекстом (уже сброшенным в лог на предыдущем Write
// или являющимся обычным нередактируемым текстом) может побайтово
// восстановить значение этого секрета.
func anySecretContains(marker string, secrets []string) bool {
	for _, sec := range secrets {
		if sec != "" && strings.Contains(sec, marker) {
			return true
		}
	}
	return false
}

// redactAll заменяет все вхождения секретов на marker ДО фиксированной точки:
// замена одного секрета может СИНТЕЗИРОВАТЬ другой из окружающего контекста
// (секрет "foo" в "xfooy" → "x"+marker+"y", а это может оказаться значением
// другого секрета "x[REDACTED]y" — codex). Повторяем полный проход, пока строка
// меняется; каждый проход убирает ≥1 секрет, поэтому цикл конечен (cap — предел
// безопасности от патологии). marker по построению не содержит секрета (условие
// (a) redactMarker), так что сам он новых вхождений не порождает — только
// контекстные стыки, которые следующий проход и добивает.
func redactAll(b []byte, secrets []string, marker string) []byte {
	maxPasses := len(b) + 1 // зафиксировать ДО цикла: при fallback-маркере "" строка
	// сжимается, а пересчёт len(b) в условии дал бы ранний выход с остатком секрета (codex).
	for pass := 0; pass < maxPasses; pass++ {
		changed := false
		for _, s := range secrets {
			if s == "" {
				continue
			}
			if bytes.Contains(b, []byte(s)) {
				b = bytes.ReplaceAll(b, []byte(s), []byte(marker))
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return b
}

func redactString(s string, secrets []string) string {
	if len(secrets) == 0 {
		return s
	}
	return string(redactAll([]byte(s), secrets, redactMarker(secrets)))
}

func secretValues(h Hook) []string {
	out := make([]string, 0, len(h.ResolvedEnv))
	for _, v := range h.ResolvedEnv {
		out = append(out, v)
	}
	return out
}
