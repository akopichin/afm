package orchestrator

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type phaseSession struct {
	SessionID string `json:"session_id"`
}

func sessionFile(stageDir, phase string) string {
	return filepath.Join(stageDir, phase+".session.json")
}

func loadOrCreateSession(stageDir, phase string) (string, error) {
	p := sessionFile(stageDir, phase)
	data, err := os.ReadFile(p)
	if err == nil {
		var s phaseSession
		if err := json.Unmarshal(data, &s); err != nil {
			return "", fmt.Errorf("parse session: %w", err)
		}
		if s.SessionID != "" {
			return s.SessionID, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read session: %w", err)
	}
	id := newUUID()
	out, _ := json.Marshal(phaseSession{SessionID: id})
	if err := os.WriteFile(p, out, 0644); err != nil {
		return "", fmt.Errorf("write session: %w", err)
	}
	return id, nil
}

func sessionExists(stageDir, phase string) bool {
	_, err := os.Stat(sessionFile(stageDir, phase))
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

func writeMcpConfig(stageDir, stageID, phase, dashURL string) (string, error) {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"flowmanager": map[string]any{
				"type": "http",
				"url":  fmt.Sprintf("%s/mcp/%s/%s", dashURL, stageID, phase),
			},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(stageDir, "mcp.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return path, nil
}
