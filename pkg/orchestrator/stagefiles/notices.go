package stagefiles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// noticeEntry — одна строка side-car notices.jsonl: UI-уведомления без
// FSM-перехода (agent_completed для отдельной фазы стадии, context_warning).
// events.jsonl намеренно не трогаем — это единственный источник правды с
// CAS-инвариантом (Store.Apply: строгая проверка current != t.From), а эти
// два типа уведомлений не соответствуют реальному переходу статуса стадии.
// Файл run-level, не per-stage.
type noticeEntry struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	StageID string    `json:"stage_id"`
	Data    any       `json:"data,omitempty"`
}

// noticeMu serializes every AppendNotice call across the process. Small
// O_APPEND writes are atomic on Linux/macOS on their own, but since script
// stages now run separate stdout- and stderr-reader goroutines (see
// executor.RunScript/pkg/orchestrator's execScript), both can call
// AppendNotice for the same run concurrently — the mutex is a robustness
// guard plus gives deterministic per-arrival ordering within this process.
// It does NOT guarantee cross-stream (stdout vs stderr) ordering in the
// resulting feed: stdout and stderr are two independent OS pipes read by two
// independent goroutines, so which line's AppendNotice call wins the mutex
// first is best-effort, not a strict interleaving guarantee.
var noticeMu sync.Mutex

// AppendNotice дописывает одну строку в <runDir>/notices.jsonl. Ошибка
// нефатальна (это вспомогательный файл для истории event feed, не источник
// правды) — молча игнорируется, как и dialog.jsonl в файловом протоколе.
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
