package main

import (
	"go/ast"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/singlechecker"
)

var Analyzer = &analysis.Analyzer{
	Name: "noStoreApplyOutsideFSM",
	Doc:  "Prohibits direct (*state.Store).Apply calls outside pkg/orchestrator/bus/fsm.go.",
	Run:  run,
}

// fsmFile — единственный файл, которому разрешён прямой (*state.Store).Apply.
// Переехал из pkg/orchestrator/fsm.go в pkg/orchestrator/bus/fsm.go (Task 4
// orchestrator-split, вынос CriticalBus/UIBus/FSM в отдельный пакет) — путь
// здесь хардкодился под старое расположение и не был упомянут в брифе задачи;
// без этой правки линтер молча продолжил бы искать старый путь и репортил
// бы FSM.Apply как нарушение.
const fsmFile = "pkg/orchestrator/bus/fsm.go"

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		fname := pass.Fset.Position(file.Pos()).Filename
		if strings.HasSuffix(fname, fsmFile) || strings.HasSuffix(fname, "_test.go") {
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
				pass.Reportf(call.Pos(), "(*state.Store).Apply must be called only via FSM in %s (got call in %s)", fsmFile, fname)
			}
			return true
		})
	}
	return nil, nil
}

func main() { singlechecker.Main(Analyzer) }
