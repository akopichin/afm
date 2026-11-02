package executor

import "strings"

// cliErrorMaxRunes ограничивает текст ошибки CLI, который попадает в причину падения задачи.
const cliErrorMaxRunes = 400

// CLIReportedError — CLI helper-а сам сообщил об ошибке корневого хода structured result-событием
// (`{"type":"result","is_error":true}`), например «Not logged in». Это не нарушение JSON-контракта ответа:
// ответа нет, а Unwrap отдаёт исходную ошибку процесса (exit status или TerminalResultError), если она была.
type CLIReportedError struct {
	// Subtype — result.subtype (error_during_execution…); может быть пустым.
	Subtype string
	// Message — result.result после редакции секретов, усечённый до cliErrorMaxRunes.
	Message string

	cause error
}

func (e *CLIReportedError) Error() string {
	msg := "CLI reported an error"
	if e.Subtype != "" {
		msg += " (" + e.Subtype + ")"
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

func (e *CLIReportedError) Unwrap() error { return e.cause }

// newCLIReportedError собирает ошибку из последнего ошибочного корневого result; red маскирует секреты.
func newCLIReportedError(t *TerminalResultError, red helperRedactor, cause error) *CLIReportedError {
	msg := strings.TrimSpace(red.text(t.Message))
	if r := []rune(msg); len(r) > cliErrorMaxRunes {
		msg = string(r[:cliErrorMaxRunes]) + "…"
	}
	return &CLIReportedError{Subtype: t.Subtype, Message: msg, cause: cause}
}
