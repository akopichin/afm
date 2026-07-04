# golang.org/x/tools (go/analysis, go/analysis/singlechecker)

Фреймворк для написания кастомных статических анализаторов (линтеров), запускаемых как самостоятельный
CLI-бинарник через `go vet -vettool=...` или напрямую. Аудитория: клеточка `tools/setstatuslinter`.

## Однопроверочный анализатор через singlechecker

Когда нужен ровно один custom-анализатор (не набор), `singlechecker.Main` — самый простой способ
получить рабочий CLI без ручной обвязки flag-парсинга:

```go
package main

import (
	"go/ast"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/singlechecker"
)

var Analyzer = &analysis.Analyzer{
	Name: "noStoreApplyOutsideFSM",
	Doc:  "Prohibits direct (*state.Store).Apply calls outside pkg/orchestrator/fsm.go.",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		fname := pass.Fset.Position(file.Pos()).Filename
		if strings.HasSuffix(fname, "pkg/orchestrator/fsm.go") || strings.HasSuffix(fname, "_test.go") {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Apply" {
				return true
			}
			tv, ok := pass.TypesInfo.Types[sel.X]
			if !ok {
				return true
			}
			t := tv.Type.String()
			if strings.HasSuffix(t, "/pkg/state.Store") || strings.HasSuffix(t, "/pkg/state.*Store") {
				pass.Reportf(call.Pos(), "(*state.Store).Apply must be called only via FSM (got call in %s)", fname)
			}
			return true
		})
	}
	return nil, nil
}

func main() { singlechecker.Main(Analyzer) }
```

## Типовой паттерн: архитектурный guard через `pass.TypesInfo`

Правило "метод X можно вызывать только из файла/пакета Y" реализуется как: обход AST в поиске
`*ast.CallExpr` → `*ast.SelectorExpr` с нужным именем метода → резолв реального типа получателя через
`pass.TypesInfo.Types[sel.X].Type.String()` (полный путь пакета, а не просто имя типа, чтобы не путать
одноимённые типы из разных пакетов) → сравнение с ожидаемым типом → `pass.Reportf(pos, ...)` при
нарушении.

## Особенности

- `pass.Reportf` не прерывает компиляцию сам по себе — код завершения не-нулевой формирует
  `singlechecker.Main`, когда есть хотя бы один репорт.
- Файлы-исключения (например, сам `fsm.go` и `_test.go`) исключаются по суффиксу пути ещё до обхода
  AST — дешевле, чем фильтровать на уровне отдельных вызовов.
