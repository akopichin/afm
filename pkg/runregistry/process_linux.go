package runregistry

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ProcessStart returns an opaque marker of when pid started: the same PID with another marker is another
// process.
func ProcessStart(pid int) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", err
	}
	// The command name in field 2 may contain spaces and parentheses, so count fields after the last ')'.
	stat := string(data)
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return "", fmt.Errorf("runregistry: malformed /proc/%d/stat", pid)
	}
	// Fields after the name start at field 3 (state); starttime is field 22.
	fields := strings.Fields(stat[end+1:])
	const starttimeIndex = 22 - 3
	if len(fields) <= starttimeIndex {
		return "", fmt.Errorf("runregistry: malformed /proc/%d/stat", pid)
	}
	return fields[starttimeIndex], nil
}
