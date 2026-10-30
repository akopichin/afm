package executor

import (
	"encoding/json"
	"strings"
)

// terminalResultMaxChars ограничивает текст причины из корневого result — он попадает в ошибку и notice.
const terminalResultMaxChars = 500

// TerminalResultError — CLI завершился с кодом 0, но его последний корневой result помечен is_error.
// Текст результата остаётся в Error(), чтобы Classify по-прежнему узнавал provider-ошибки (rate limit и т.п.).
type TerminalResultError struct {
	Subtype string
	Message string
}

func (e *TerminalResultError) Error() string {
	msg := "agent finished with an error result"
	if e.Subtype != "" && e.Subtype != "success" { // provider-ошибки часто приходят с subtype=success
		msg += " (" + e.Subtype + ")"
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// terminalResult запоминает последний корневой {"type":"result"} одного вызова. Ошибки tool/subagent и
// промежуточные error-строки сюда не попадают: итог определяет только последний root result.
type terminalResult struct {
	seen    bool
	isError bool
	subtype string
	message string
}

func (t *terminalResult) observe(line string) {
	var env struct {
		Type    string `json:"type"`
		IsError bool   `json:"is_error"`
		Subtype string `json:"subtype"`
		Result  string `json:"result"`
	}
	if json.Unmarshal([]byte(line), &env) != nil || env.Type != "result" {
		return
	}
	msg := strings.TrimSpace(env.Result)
	if runes := []rune(msg); len(runes) > terminalResultMaxChars { // режем по символам: не рвём UTF-8
		msg = string(runes[:terminalResultMaxChars]) + "…"
	}
	*t = terminalResult{seen: true, isError: env.IsError, subtype: env.Subtype, message: msg}
}

// err возвращает типизированную ошибку, если последний корневой result был ошибочным, иначе nil.
func (t *terminalResult) err() error {
	if !t.seen || !t.isError {
		return nil
	}
	return &TerminalResultError{Subtype: t.subtype, Message: t.message}
}
