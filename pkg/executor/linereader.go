package executor

import (
	"bufio"
	"io"
	"strings"
)

// lineReader reads lines from r, calling fn for each line.
// Returns when r is exhausted or fn returns false.
func lineReader(r io.Reader, fn func(line string) bool) error {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if !fn(line) {
				return nil
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
