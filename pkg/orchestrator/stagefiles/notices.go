package stagefiles

import (
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

// noticeMu serializes every AppendNotice call across the process.
var noticeMu sync.Mutex

// AppendNotice дописывает одну строку в <runDir>/notices.jsonl.
func AppendNotice(runDir, stageID, eventType string, data any) {
	entry := noticeEntry{Time: time.Now(), Type: eventType, StageID: stageID, Data: data}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	noticeMu.Lock()
	defer noticeMu.Unlock()
	f, err := os.OpenFile(filepath.Join(runDir, "notices.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}
