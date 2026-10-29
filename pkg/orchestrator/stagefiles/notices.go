package stagefiles

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// noticeEntry — одна строка side-car notices.jsonl: UI-уведомления без FSM-перехода. Файл run-level, не
// per-stage.
type noticeEntry struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	StageID string    `json:"stage_id"`
	Data    any       `json:"data,omitempty"`
}

// noticeMu serializes every notice write across the process.
var noticeMu sync.Mutex

// AppendNotice дописывает одну строку в <runDir>/notices.jsonl; ошибка записи не возвращается — notice
// best-effort.
func AppendNotice(runDir, stageID, eventType string, data any) {
	_ = WriteNotice(runDir, stageID, eventType, data)
}

// WriteNotice is AppendNotice that reports whether the line reached the file, for a notice whose live copy
// must not outrun its durable one.
func WriteNotice(runDir, stageID, eventType string, data any) error {
	entry := noticeEntry{Time: time.Now(), Type: eventType, StageID: stageID, Data: data}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	noticeMu.Lock()
	defer noticeMu.Unlock()
	f, err := os.OpenFile(filepath.Join(runDir, "notices.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// HasNotice reports whether notices.jsonl holds a notice of eventType for stageID whose data has string field
// equal to value. A missing or unreadable file means no.
func HasNotice(runDir, stageID, eventType, field, value string) bool {
	f, err := os.Open(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		return false
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadBytes('\n')
		var entry struct {
			Type    string         `json:"type"`
			StageID string         `json:"stage_id"`
			Data    map[string]any `json:"data"`
		}
		if json.Unmarshal(line, &entry) == nil && entry.Type == eventType && entry.StageID == stageID && entry.Data[field] == value {
			return true
		}
		if err != nil {
			return false
		}
	}
}
