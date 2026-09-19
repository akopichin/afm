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
