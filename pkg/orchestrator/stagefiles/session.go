package stagefiles

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type phaseSession struct {
	SessionID string `json:"session_id"`
}

// SessionFile returns the path to the phase's session-id side-car file.
func SessionFile(stageDir, phase string) string {
	return filepath.Join(stageDir, phase+".session.json")
}

// LoadOrCreateSession returns the existing session id for stageDir/phase,
// creating and persisting a fresh one if none exists yet. Creation never
// overwrites an existing id: a concurrent creator that loses the race adopts
// the id already published (a superseded turn must not replace its successor's).
func LoadOrCreateSession(stageDir, phase string) (string, error) {
	p := SessionFile(stageDir, phase)
	if id, err := readSessionID(p); err != nil || id != "" {
		return id, err
	}
	id := newUUID()
	out, _ := json.Marshal(phaseSession{SessionID: id})
	switch err := publishNew(p, out); {
	case err == nil:
		return id, nil
	case errors.Is(err, fs.ErrExist):
		// Файл появился после нашего чтения: берём чужой id, свой не пишем.
		winner, readErr := readSessionID(p)
		if readErr != nil || winner != "" {
			return winner, readErr
		}
		return "", fmt.Errorf("write session: %s exists without session id", p)
	default:
		return "", fmt.Errorf("write session: %w", err)
	}
}

// readSessionID returns the id stored in path, or "" when the file is missing or has no id.
func readSessionID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read session: %w", err)
	}
	var s phaseSession
	if err := json.Unmarshal(data, &s); err != nil {
		return "", fmt.Errorf("parse session: %w", err)
	}
	return s.SessionID, nil
}

// publishNew атомарно создаёт path с содержимым data: временный файл + hard link.
// Если path уже существует, возвращает fs.ErrExist и ничего не меняет;
// читатель никогда не видит наполовину записанный файл.
func publishNew(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Link(tmp.Name(), path)
}

// SessionExists reports whether a session-id file already exists for stageDir/phase.
func SessionExists(stageDir, phase string) bool {
	_, err := os.Stat(SessionFile(stageDir, phase))
	return err == nil
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
