package verify

// RunOutcome разделяет два независимых исхода одного запуска верификатора: успешно ли отработал сам
// процесс и что решила модель.
type RunOutcome struct {
	// ProcessOK — true, если процесс верификатора завершился штатно (не по
	// таймауту/сигналу) и его вывод удалось прочитать.
	ProcessOK bool
	// TimedOut — процесс превысил отведённое время и был прерван.
	TimedOut bool
	// Interrupted — запуск прерван извне (отмена контекста, пауза стадии).
	Interrupted bool
	// ProtocolErr — ответ модели отсутствует, повреждён или противоречив
	// (см. DecodeModelResult). Не путать с ошибкой самого процесса.
	ProtocolErr error
	// Result — разобранный и провалидированный ответ модели. Заполняется
	// ТОЛЬКО когда ProtocolErr == nil и ответ успешно прошёл DecodeModelResult.
	Result *ModelResult
}
