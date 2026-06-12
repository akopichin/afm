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
				pass.Reportf(call.Pos(), "(*state.Store).Apply must be called only via FSM in pkg/orchestrator/fsm.go (got call in %s)", fname)
			}
			return true
		})
	}
	return nil, nil
}

func main() { singlechecker.Main(Analyzer) }
