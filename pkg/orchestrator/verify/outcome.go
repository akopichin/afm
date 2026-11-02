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
	// NativeFallback — почему запрошенный native-режим схемы не использован ("" — использован или не
	// запрошен). Строка, а не тип executor: executor импортирует этот пакет.
	NativeFallback string
	// NativeStarted — процесс действительно стартовал в native-режиме: только такой запуск мог получить отказ
	// провайдера от этого режима.
	NativeStarted bool
	// StructuredPayload — ответ взят из structured payload Claude, а не из текстового блока.
	StructuredPayload bool
	// NativeEnforced — CLI заставил модель вызвать StructuredOutput после хода, закончившегося текстом:
	// payload вынужденный и может быть выдуманным.
	NativeEnforced bool
}
