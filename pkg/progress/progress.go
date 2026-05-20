package progress

import (
	"fmt"
	"os"
	"time"
)

// Logger writes timestamped messages to a file (append-only) and stdout.
type Logger struct {
	f         *os.File
	startTime time.Time
}

// NewLogger opens (or creates) a log file. If the file exists without a
// completion footer, a restart separator is appended.
func NewLogger(path string) (*Logger, error) {
	existing, _ := os.ReadFile(path)
	needsSeparator := len(existing) > 0

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	lg := &Logger{f: f, startTime: time.Now()}

	if needsSeparator {
		sep := fmt.Sprintf("\n--- restarted at %s ---\n", time.Now().Format("06-01-02 15:04:05"))
		lg.write(sep)
	}
	return lg, nil
}

// Log writes a timestamped line to the log file and stdout.
func (l *Logger) Log(msg string) {
	line := fmt.Sprintf("%s  %s\n", time.Now().Format("06-01-02 15:04:05"), msg)
	l.write(line)
	fmt.Print(line)
}

// LogStart writes a start banner with agent type, stage name, and timestamp.
func (l *Logger) LogStart(agentType, stageName string) {
	line := fmt.Sprintf("=== %s agent | stage: %s | started: %s ===\n",
		agentType, stageName, l.startTime.Format("2006-01-02 15:04:05"))
	l.write(line)
	fmt.Print(line)
}

// LogAction writes a timestamped action line (tool name + detail).
func (l *Logger) LogAction(toolName, detail string) {
	line := fmt.Sprintf("%s  %-6s  %s\n",
		time.Now().Format("15:04:05"), toolName, detail)
	l.write(line)
	fmt.Print(line)
}

// LogEnd writes a completion or failure banner with elapsed duration.
func (l *Logger) LogEnd(err error) {
	duration := time.Since(l.startTime).Round(time.Second)
	var line string
	if err == nil {
		line = fmt.Sprintf("=== completed | %s | duration: %s ===\n",
			time.Now().Format("2006-01-02 15:04:05"), duration)
	} else {
		line = fmt.Sprintf("=== FAILED | %s | duration: %s | %s ===\n",
			time.Now().Format("2006-01-02 15:04:05"), duration, err)
	}
	l.write(line)
	fmt.Print(line)
}

func (l *Logger) write(s string) {
	l.f.WriteString(s)
	l.f.Sync() //nolint:errcheck // flush for concurrent readers
}

// Close closes the underlying file.
func (l *Logger) Close() error {
	return l.f.Close()
}

// Lock is a file-based exclusive lock.
type Lock struct {
	path string
	f    *os.File
}

// NewLock creates a Lock handle for the given path (does not acquire).
func NewLock(path string) (*Lock, error) {
	return &Lock{path: path}, nil
}
