package executor

import (
	"encoding/json"
	"os"
	"strings"
)

// askUserToolName — имя MCP-инструмента ask_user в stream-json логе.
const askUserToolName = "mcp__afm__ask_user"

// TranscriptItem — один элемент диалоговой ленты из stream-json лога:
// либо текст ассистента (Text != ""), либо вызов ask_user (AskUserID != "").
type TranscriptItem struct {
	Text      string
	AskUserID string
}

// DialogTranscript читает stream-json лог и возвращает текстовые сообщения
// ассистента и вызовы ask_user в порядке появления. Повторные вызовы
// ask_user с тем же id (polling-ретраи) схлопываются в первое вхождение.
// Отсутствующий или нечитаемый файл даёт пустой список.
func DialogTranscript(jsonlPath string) []TranscriptItem {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var items []TranscriptItem
	seen := map[string]bool{}
	_ = lineReader(f, func(line string) bool {
		ev, ok := parseStreamEvent(line)
		if !ok {
			return true
		}
		for _, c := range ev.Message.Content {
			switch {
			case c.Type == contentTypeText:
				if strings.TrimSpace(c.Text) == "" {
					continue
				}
				items = append(items, TranscriptItem{Text: c.Text})
			case c.Type == contentTypeToolUse && c.Name == askUserToolName:
				var inp struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(c.Input, &inp) != nil || inp.ID == "" || seen[inp.ID] {
					continue
				}
				seen[inp.ID] = true
				items = append(items, TranscriptItem{AskUserID: inp.ID})
			default:
				// прочие tool_use в диалоговую ленту не попадают
			}
		}
		return true
	})
	return items
}
