package executor

import (
	"bufio"
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
	sc := bufio.NewScanner(f)
	// Stream-json lines carrying full Write-tool content easily exceed the
	// scanner's default 64KB limit (same reasoning as WrittenFiles).
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		ev, ok := parseStreamEvent(sc.Text())
		if !ok {
			continue
		}
		for _, c := range ev.Message.Content {
			if tool, detail, actionOK := contentToAction(c, 0); actionOK {
				lines = append(lines, fmt.Sprintf("%-6s  %s", tool, detail))
			}
		}
	}
	return lines
}
