package executor

import (
	"bufio"
	"io"
)

// lineReader reads lines from r, calling fn for each line.
// Returns when r is exhausted or fn returns false.
func lineReader(r io.Reader, fn func(line string) bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB per line
	for scanner.Scan() {
		if !fn(scanner.Text()) {
			return nil
		}
	}
	return scanner.Err()
}
