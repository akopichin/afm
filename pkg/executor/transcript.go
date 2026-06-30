package executor

import (
	"bufio"
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
	sc := bufio.NewScanner(f)
	// Строки stream-json содержат полный контент Write-вызовов и легко
	// превышают дефолтный лимит сканера в 64 КБ.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		ev, ok := parseStreamEvent(sc.Text())
		if !ok {
			continue
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
	}
	return items
}
