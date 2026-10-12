package executor

import (
	"fmt"
	"os"
)

// RenderActions reads full tool/text actions from stream-json for retry context, without display
// truncation; unreadable logs return nil.
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
