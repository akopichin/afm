package executor

import (
	"fmt"
	"os"
)

// RenderActions parses a stage's raw stream-json log (<phase>.jsonl) and
// returns one line per tool/text action, WITHOUT the Config.TruncateOutput
// limit applied to <phase>.log — the log's detail is intentionally abbreviated
// for the dashboard/event feed, but a retry continuation prompt needs to see
// what the stage actually did in full. Missing or unreadable file returns nil.
func RenderActions(jsonlPath string) []string {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var lines []string
	_ = lineReader(f, func(line string) bool {
		ev, ok := parseStreamEvent(line)
		if !ok {
			return true
		}
		for _, c := range ev.Message.Content {
			if tool, detail, actionOK := contentToAction(c, 0); actionOK {
				lines = append(lines, fmt.Sprintf("%-6s  %s", tool, detail))
			}
		}
		return true
	})
	return lines
}
