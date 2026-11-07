package assistantstest

// Общие JSON-фикстуры ответа помощника (envelope §9.3) для тестов пакетов
// assistants/orchestrator/server — без обращения к платной модели. Формат
// соответствует schema/assistant-result.schema.json; держи фикстуры и схему
// в синхроне при правках.
const (
	// FixtureValidResult — минимальный валидный kind:"result" без находок.
	FixtureValidResult = `{
		"schema_version": 1,
		"kind": "result",
		"verdict": "pass",
		"summary": "Looked at the diff, no issues found.",
		"context_version": "ctx-1"
	}`

	// FixtureResultWithFindings — pass с ненулевыми, но не блокирующими
	// находками (severity ниже blocker не мешает verdict:"pass").
	FixtureResultWithFindings = `{
		"schema_version": 1,
		"kind": "result",
		"verdict": "pass",
		"summary": "Mostly fine, one minor nit.",
		"findings": [
			{"id": "f1", "severity": "minor", "path": "main.go", "line_start": 10, "line_end": 12, "detail": "consider renaming this variable"}
		],
		"evidence": ["read main.go"],
		"context_version": "ctx-1"
	}`

	// FixtureNeedsChangesResult — needs_changes с блокирующей находкой.
	FixtureNeedsChangesResult = `{
		"schema_version": 1,
		"kind": "result",
		"verdict": "needs_changes",
		"summary": "Found a real bug.",
		"findings": [
			{"id": "f1", "severity": "blocker", "path": "main.go", "line_start": 42, "line_end": 42, "detail": "nil pointer dereference on empty input"}
		],
		"evidence": ["read main.go", "ran go vet"],
		"suggested_change": "add a nil check before dereferencing",
		"context_version": "ctx-1"
	}`

	// FixtureInvalidJSON — синтаксически битый JSON (незакрытая структура).
	FixtureInvalidJSON = `{
		"schema_version": 1,
		"kind": "result",
		"verdict": "pass"
	`

	// FixtureMarkdownFenced — валидный envelope, но обёрнутый в markdown
	// code-fence, как иногда отвечает модель вопреки инструкции. Декодеру
	// это нужно либо отвергнуть, либо явно распаковать — T0.3 фиксирует его
	// как «не декодируется напрямую».
	FixtureMarkdownFenced = "```json\n" + FixtureValidResult + "\n```"

	// FixtureResultWithSecret — валидный envelope с похожим на секрет текстом
	// внутри summary; значение заведомо фиктивное, нужно только форме
	// редакции (другие этапы). Не настоящий ключ.
	//nolint:gosec // фиктивный "похожий на секрет" текст — фикстура для теста редакции, не настоящий ключ.
	FixtureResultWithSecret = `{
		"schema_version": 1,
		"kind": "result",
		"verdict": "needs_changes",
		"summary": "Found a hardcoded credential: ANTHROPIC_API_KEY=sk-ant-api03-FAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE in config.go",
		"findings": [
			{"id": "f1", "severity": "blocker", "path": "config.go", "line_start": 5, "line_end": 5, "detail": "do not commit API keys"}
		],
		"context_version": "ctx-1"
	}`

	// FixtureDelegateResponse — kind:"delegate": вместо verdict/findings несёт
	// children[]/continuation. Полная семантика — этап 4a; здесь фикстура
	// нужна только чтобы декодер уже умел различать два kind по дискриминатору.
	FixtureDelegateResponse = `{
		"schema_version": 1,
		"kind": "delegate",
		"summary": "Splitting this into a review and a security pass.",
		"children": [
			{"profile_id": "reviewer", "title": "Review the diff", "instruction": "Review pkg/foo for correctness.", "mode": "advisory", "trigger": "one_shot"},
			{"profile_id": "security", "title": "Security pass", "instruction": "Check pkg/foo for injection issues.", "mode": "required", "trigger": "one_shot"}
		],
		"continuation": {"trigger": "on_completion", "note": "resume once both children report back"}
	}`
)
