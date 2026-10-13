package orchestrator

import (
	"strings"

	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

// dialogDigestBudgetBytes — верхняя граница размера дайджеста Q&A, вклеиваемого в промпт агента,
// стартующего без --resume.
const dialogDigestBudgetBytes = 8 * 1024

// dialogDigestHeading — заголовок блока дайджеста прошлых вопросов/ответов пользователя.
const dialogDigestHeading = "## Prior Q&A with the user (decisions already made — honor them)"

// dialogDigestTruncationMarker дописывается, когда даже самая свежая пара Q&A не помещается в бюджет
// целиком — явный сигнал агенту, что текст неполный.
const dialogDigestTruncationMarker = "\n\n… (обрезано: показаны последние вопросы)\n"

// dialogDigestBlock рендерит отвеченную историю Q&A стадии для phase как блок для вклейки в промпт —
// нужен агенту, стартующему БЕЗ --resume, чтобы не переспрашивать уже решённое и не терять решения
// пользователя. Новейшие пары сохраняются первыми в рамках байтового бюджета. Возвращает "", когда
// показывать нечего (best-effort: любая ошибка чтения тоже даёт "" — дайджест не должен валить попытку).
func dialogDigestBlock(stageDir, phase string) string {
	entries, err := mcp.ReadDialog(mcp.DialogPath(stageDir, phase))
	if err != nil {
		return ""
	}

	pairs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Answer == nil {
			continue // ещё не отвечено — это и есть вопрос, который агент вот-вот задаст снова
		}
		pairs = append(pairs, renderDialogPair(e))
	}
	if len(pairs) == 0 {
		return ""
	}

	body := strings.Join(selectDialogPairs(pairs), "\n\n")
	return "\n\n" + dialogDigestHeading + "\n\n" + body
}

// renderDialogPair рендерит один отвеченный вопрос в компактный текстовый блок.
func renderDialogPair(e mcp.Entry) string {
	var b strings.Builder
	b.WriteString("Q: ")
	b.WriteString(e.Question)
	if len(e.Options) > 0 {
		b.WriteString("\n(options: ")
		b.WriteString(strings.Join(e.Options, " | "))
		b.WriteString(")")
	}
	b.WriteString("\nA: ")
	b.WriteString(*e.Answer)
	if e.AutoAnswered {
		b.WriteString(" (auto-answered)")
	}
	return b.String()
}

// selectDialogPairs идёт от конца (самые свежие первыми), накапливая пары, пока суммарный размер
// укладывается в budget байт, затем возвращает отобранные пары в хронологическом порядке. Если не влезает
// даже единственная самая свежая пара — она обрезается TruncateUTF8 и возвращается одна.
func selectDialogPairs(pairs []string) []string {
	var kept []string
	total := 0
	for i := len(pairs) - 1; i >= 0; i-- {
		p := pairs[i]
		sep := 0
		if len(kept) > 0 {
			sep = len("\n\n")
		}
		if total+sep+len(p) > dialogDigestBudgetBytes {
			break
		}
		total += sep + len(p)
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		freshest := pairs[len(pairs)-1]
		return []string{stagefiles.TruncateUTF8(freshest, dialogDigestBudgetBytes, dialogDigestTruncationMarker)}
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return kept
}
