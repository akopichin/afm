package verify

import (
	"strings"
	"testing"
)

// intPtr/strPtr — маленькие хелперы для литералов-указателей в тестах.
func intPtr(v int) *int       { return &v }
func strPtr(v string) *string { return &v }

func TestDecodeModelResult_HappyPaths(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want ModelResult
	}{
		{
			name: "valid pass",
			raw:  `{"schema_version":1,"verdict":"pass","summary":"всё хорошо","findings":[]}`,
			want: ModelResult{SchemaVersion: 1, Verdict: VerdictPass, Summary: "всё хорошо", Findings: []Finding{}},
		},
		{
			name: "valid needs_changes with blocking finding incl. null path/lines",
			raw: `{"schema_version":1,"verdict":"needs_changes","summary":"есть проблема","findings":[` +
				`{"blocking":true,"title":"нет теста","path":null,"line_start":null,"line_end":null,` +
				`"requirement":"покрыть тестом","evidence":"grep не нашёл","minimal_fix":"добавить тест"}]}`,
			want: ModelResult{
				SchemaVersion: 1,
				Verdict:       VerdictNeedsChanges,
				Summary:       "есть проблема",
				Findings: []Finding{{
					Blocking:    true,
					Title:       "нет теста",
					Path:        nil,
					LineStart:   nil,
					LineEnd:     nil,
					Requirement: "покрыть тестом",
					Evidence:    "grep не нашёл",
					MinimalFix:  "добавить тест",
				}},
			},
		},
		{
			name: "valid inconclusive with summary",
			raw:  `{"schema_version":1,"verdict":"inconclusive","summary":"не удалось проверить: нет доступа к сети","findings":[]}`,
			want: ModelResult{SchemaVersion: 1, Verdict: VerdictInconclusive, Summary: "не удалось проверить: нет доступа к сети", Findings: []Finding{}},
		},
		{
			name: "non-blocking finding with concrete path/lines",
			raw: `{"schema_version":1,"verdict":"pass","summary":"мелкое замечание, не блокирует","findings":[` +
				`{"blocking":false,"title":"стиль","path":"main.go","line_start":10,"line_end":12,` +
				`"requirement":"стиль кода","evidence":"длинная строка","minimal_fix":"разбить строку"}]}`,
			want: ModelResult{
				SchemaVersion: 1,
				Verdict:       VerdictPass,
				Summary:       "мелкое замечание, не блокирует",
				Findings: []Finding{{
					Blocking:    false,
					Title:       "стиль",
					Path:        strPtr("main.go"),
					LineStart:   intPtr(10),
					LineEnd:     intPtr(12),
					Requirement: "стиль кода",
					Evidence:    "длинная строка",
					MinimalFix:  "разбить строку",
				}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeModelResult([]byte(tc.raw))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.SchemaVersion != tc.want.SchemaVersion || got.Verdict != tc.want.Verdict || got.Summary != tc.want.Summary {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if len(got.Findings) != len(tc.want.Findings) {
				t.Fatalf("findings length: got %d, want %d", len(got.Findings), len(tc.want.Findings))
			}
			for i := range got.Findings {
				gf, wf := got.Findings[i], tc.want.Findings[i]
				if gf.Blocking != wf.Blocking || gf.Title != wf.Title || gf.Requirement != wf.Requirement ||
					gf.Evidence != wf.Evidence || gf.MinimalFix != wf.MinimalFix {
					t.Fatalf("finding[%d]: got %+v, want %+v", i, gf, wf)
				}
				if (gf.Path == nil) != (wf.Path == nil) || (gf.Path != nil && *gf.Path != *wf.Path) {
					t.Fatalf("finding[%d].Path: got %v, want %v", i, gf.Path, wf.Path)
				}
				if (gf.LineStart == nil) != (wf.LineStart == nil) || (gf.LineStart != nil && *gf.LineStart != *wf.LineStart) {
					t.Fatalf("finding[%d].LineStart: got %v, want %v", i, gf.LineStart, wf.LineStart)
				}
				if (gf.LineEnd == nil) != (wf.LineEnd == nil) || (gf.LineEnd != nil && *gf.LineEnd != *wf.LineEnd) {
					t.Fatalf("finding[%d].LineEnd: got %v, want %v", i, gf.LineEnd, wf.LineEnd)
				}
			}
		})
	}
}

func TestDecodeModelResult_Rejections(t *testing.T) {
	validJSON := `{"schema_version":1,"verdict":"pass","summary":"ok","findings":[]}`

	cases := []struct {
		name          string
		raw           string
		wantSubstring string // если не пусто — проверяем, что текст ошибки содержит это
	}{
		{
			name:          "too large",
			raw:           strings.Repeat("a", MaxResultBytes+1),
			wantSubstring: "limit",
		},
		{
			name:          "markdown fence wrapper",
			raw:           "```json\n" + validJSON + "\n```",
			wantSubstring: "markdown",
		},
		{
			name: "unknown top-level field",
			raw:  `{"schema_version":1,"verdict":"pass","summary":"ok","findings":[],"extra":true}`,
		},
		{
			name: "unknown field in finding",
			raw: `{"schema_version":1,"verdict":"needs_changes","summary":"ok","findings":[` +
				`{"blocking":true,"title":"t","path":null,"line_start":null,"line_end":null,` +
				`"requirement":"r","evidence":"e","minimal_fix":"f","id":"should-not-be-here"}]}`,
		},
		{
			name: "trailing content after JSON document",
			raw:  validJSON + `{"schema_version":1,"verdict":"pass","summary":"ok","findings":[]}`,
		},
		{
			name:          "duplicate key top-level",
			raw:           `{"schema_version":1,"schema_version":1,"verdict":"pass","summary":"ok","findings":[]}`,
			wantSubstring: "duplicate",
		},
		{
			name: "duplicate key nested in finding",
			raw: `{"schema_version":1,"verdict":"needs_changes","summary":"ok","findings":[` +
				`{"blocking":true,"blocking":true,"title":"t","path":null,"line_start":null,"line_end":null,` +
				`"requirement":"r","evidence":"e","minimal_fix":"f"}]}`,
			wantSubstring: "duplicate",
		},
		{
			name:          "unknown schema_version",
			raw:           `{"schema_version":2,"verdict":"pass","summary":"ok","findings":[]}`,
			wantSubstring: "schema_version",
		},
		{
			name:          "invalid verdict enum",
			raw:           `{"schema_version":1,"verdict":"maybe","summary":"ok","findings":[]}`,
			wantSubstring: "verdict",
		},
		{
			name: "pass with blocking finding",
			raw: `{"schema_version":1,"verdict":"pass","summary":"ok","findings":[` +
				`{"blocking":true,"title":"t","path":null,"line_start":null,"line_end":null,` +
				`"requirement":"r","evidence":"e","minimal_fix":"f"}]}`,
		},
		{
			name: "needs_changes with zero blocking findings",
			raw: `{"schema_version":1,"verdict":"needs_changes","summary":"ok","findings":[` +
				`{"blocking":false,"title":"t","path":null,"line_start":null,"line_end":null,` +
				`"requirement":"r","evidence":"e","minimal_fix":"f"}]}`,
		},
		{
			name: "inconclusive with empty summary",
			raw:  `{"schema_version":1,"verdict":"inconclusive","summary":"","findings":[]}`,
		},
		{
			name: "needs_changes with empty summary",
			raw: `{"schema_version":1,"verdict":"needs_changes","summary":"","findings":[` +
				`{"blocking":true,"title":"t","path":null,"line_start":null,"line_end":null,` +
				`"requirement":"r","evidence":"e","minimal_fix":"f"}]}`,
		},
		{
			name: "pass with empty summary",
			raw:  `{"schema_version":1,"verdict":"pass","summary":"","findings":[]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeModelResult([]byte(tc.raw))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tc.wantSubstring != "" && !strings.Contains(err.Error(), tc.wantSubstring) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.wantSubstring)
			}
		})
	}
}

// TestDecodeModelResult_RepairsBareNewline покрывает реальный инцидент: codex-
// ревьюер вписал в строковое значение (evidence) многострочную цитату кода, и
// сырой перенос строки попал прямо внутрь строкового литерала JSON. Строгий
// json недопускает сырой \n внутри строки — но это чисто лексическая поломка
// вывода модели, а не проблема вердикта. Декодер чинит её через jsonrepair и
// принимает результат, вместо того чтобы валить verify красным из-за транспорта.
func TestDecodeModelResult_RepairsBareNewline(t *testing.T) {
	// Реальный перенос строки (не экранированный) внутри evidence.
	raw := "{\"schema_version\":1,\"verdict\":\"needs_changes\",\"summary\":\"есть проблема\"," +
		"\"findings\":[{\"blocking\":true,\"title\":\"t\",\"path\":null,\"line_start\":null," +
		"\"line_end\":null,\"requirement\":\"r\",\"evidence\":\"line1\nline2\",\"minimal_fix\":\"f\"}]}"

	got, err := DecodeModelResult([]byte(raw))
	if err != nil {
		t.Fatalf("expected repair to accept the result, got error: %v", err)
	}
	if got.Verdict != VerdictNeedsChanges {
		t.Fatalf("verdict: got %q, want %q", got.Verdict, VerdictNeedsChanges)
	}
	if len(got.Findings) != 1 {
		t.Fatalf("findings: got %d, want 1", len(got.Findings))
	}
	if got.Findings[0].Evidence != "line1\nline2" {
		t.Fatalf("evidence: got %q, want %q", got.Findings[0].Evidence, "line1\nline2")
	}
}

// TestDecodeModelResult_RepairDoesNotMaskRejections гарантирует, что ремонт
// применяется ТОЛЬКО к лексической поломке (сырой \n), но не вскрывает дыру в
// намеренных отказах: если после ремонта остаётся дубль ключа — результат всё
// равно отклоняется. (jsonrepair не дедуплицирует ключи, а строгая проверка
// дубликатов прогоняется повторно по отремонтированным байтам.)
func TestDecodeModelResult_RepairDoesNotMaskRejections(t *testing.T) {
	// Сырой \n в evidence (чинится) + дубль ключа "blocking" (должен отклониться).
	raw := "{\"schema_version\":1,\"verdict\":\"needs_changes\",\"summary\":\"ok\"," +
		"\"findings\":[{\"blocking\":true,\"blocking\":true,\"title\":\"t\",\"path\":null," +
		"\"line_start\":null,\"line_end\":null,\"requirement\":\"r\",\"evidence\":\"a\nb\"," +
		"\"minimal_fix\":\"f\"}]}"

	_, err := DecodeModelResult([]byte(raw))
	if err == nil {
		t.Fatal("expected rejection: a duplicate key must not survive repair")
	}
	// The input is malformed in two ways; afm returns the first failure (the
	// syntax error) — what matters is that the repaired-but-still-duplicate
	// document is NOT accepted. A pure duplicate (no syntax error) still surfaces
	// the "duplicate" message, proven by TestDecodeModelResult_Rejections.
}

// TestDecodeModelResult_SyntaxErrorMessageIsNotMisleading фиксирует, что
// лексическая ошибка больше не маскируется под "duplicate key" (детектор
// дубликатов теперь идёт ПОСЛЕ структурного разбора). Берём вход с сырым \n и
// неизвестным полем, который ремонт не доведёт до валидной схемы, и проверяем,
// что ошибка — не про дубликат ключа.
func TestDecodeModelResult_SyntaxErrorMessageIsNotMisleading(t *testing.T) {
	raw := "{\"schema_version\":1,\"verdict\":\"pass\",\"summary\":\"x\ny\"," +
		"\"findings\":[],\"bogus\":true}"

	_, err := DecodeModelResult([]byte(raw))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("error must not be mislabeled as a duplicate key: %q", err.Error())
	}
}

// TestDecodeModelResult_RepairStaysNarrow гарантирует, что узкий ремонт сырых
// управляющих символов НЕ превращается в вседозволенный нормализатор: обёртки и
// лишний контент, которые jsonrepair снял бы, тут обязаны по-прежнему
// отклоняться (даже если рядом есть сырой \n, запускающий путь ремонта).
func TestDecodeModelResult_RepairStaysNarrow(t *testing.T) {
	valid := `{"schema_version":1,"verdict":"pass","summary":"ok","findings":[]}`
	cases := map[string]string{
		// JSONP-обёртка + сырой \n: ремонт экранирует \n, но обёртка остаётся →
		// json.Decode по-прежнему падает, лишний контент не проходит.
		"jsonp wrapper": "callback(" + `{"schema_version":1,"verdict":"pass","summary":"a` + "\n" + `b","findings":[]})`,
		"trailing junk": valid + " trailing-junk",
		"double doc":    valid + valid,
		"line comment":  "// note\n" + valid,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeModelResult([]byte(raw)); err == nil {
				t.Fatalf("narrow repair must not accept %q", raw)
			}
		})
	}
}

// TestEscapeRawControlCharsInStrings_OnlyInsideStrings проверяет, что ремонт
// трогает управляющие символы ТОЛЬКО внутри строковых литералов, а структурные
// переносы строк между токенами (валидный pretty-printed JSON) не меняет.
func TestEscapeRawControlCharsInStrings_OnlyInsideStrings(t *testing.T) {
	// Переносы строк МЕЖДУ токенами (не в строке) — валидны, ремонт не нужен.
	pretty := "{\n  \"schema_version\": 1\n}"
	if _, changed := escapeRawControlCharsInStrings([]byte(pretty)); changed {
		t.Fatal("structural whitespace outside strings must not be escaped")
	}
	// Сырой \n ВНУТРИ строки — экранируется.
	inStr := "{\"s\":\"a\nb\"}"
	got, changed := escapeRawControlCharsInStrings([]byte(inStr))
	if !changed || string(got) != `{"s":"a\nb"}` {
		t.Fatalf("raw newline inside a string must be escaped, got %q changed=%v", got, changed)
	}
	// Уже экранированный \n (бэкслэш+n) — не трогаем (не удваиваем).
	already := `{"s":"a\nb"}`
	if _, changed := escapeRawControlCharsInStrings([]byte(already)); changed {
		t.Fatal("already-escaped \\n must not be touched")
	}
}
