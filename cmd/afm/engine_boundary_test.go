package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	orchestratorPath = "github.com/akopichin/afm-engine/internal/orchestrator"
	statePath        = "github.com/akopichin/afm-engine/internal/state"
	mcpPath          = "github.com/akopichin/afm/contracts/mcp"
	stagefilesPath   = "github.com/akopichin/afm-engine/internal/orchestrator/stagefiles"
)

// facadeBypasses are the package symbols (by import path, so an alias does not hide them) that
// transports must not use: they reach around the engine facade.
var facadeBypasses = map[string]map[string]bool{
	orchestratorPath: {"Orchestrator": true, "New": true},
	statePath:        {"Open": true, "Store": true, "SavePreNote": true},
	mcpPath:          {"WriteAnswerFile": true, "AppendAnswer": true},
	stagefilesPath:   {"AppendNotice": true},
	"os":             {"WriteFile": true, "Create": true, "OpenFile": true, "Rename": true},
}

// facadeExceptions lists the exact operations a transport file may still use outside the run
// directory (flow templates, .gitignore, the controlling terminal). Anything else in the file is checked.
var facadeExceptions = map[string]map[string]bool{
	"cmd/afm/init.go":          {"os.WriteFile": true, "os.OpenFile": true},
	"cmd/afm/run_dashboard.go": {"os.OpenFile": true},
	// The flow file lives outside the run directory; the budget export replaces it atomically.
	"cmd/afm/run_budget_export.go": {"os.Rename": true},
}

// Transports reach the run only through the engine facade; these selectors would bypass it.
func TestBoundary_TransportsUseEngineFacade(t *testing.T) {
	for _, dir := range []string{"../../cmd/afm", "../../pkg/server"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			rel := strings.TrimPrefix(filepath.ToSlash(file), "../../")
			for _, v := range facadeViolations(t, file, src, facadeExceptions[rel]) {
				t.Error(v)
			}
		}
	}
}

// facadeViolations reports every use in src that bypasses the engine facade, except the allowed
// "pkg.Symbol" operations.
func facadeViolations(t *testing.T, file string, src []byte, allowed map[string]bool) []string {
	t.Helper()
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	// Local name -> import path, so aliased imports are recognized.
	imports := map[string]string{}
	var out []string
	for _, imp := range parsed.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("import path %s: %v", imp.Path.Value, err)
		}
		name := filepath.Base(path)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "." && facadeBypasses[path] != nil {
			out = append(out, fmt.Sprintf("%s: dot import of %s hides facade bypasses", fset.Position(imp.Pos()), path))
		}
		imports[name] = path
	}
	ast.Inspect(parsed, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pos := fset.Position(sel.Pos())
		if ident, ok := sel.X.(*ast.Ident); ok {
			if path, found := imports[ident.Name]; found && facadeBypasses[path][sel.Sel.Name] {
				name := filepath.Base(path) + "." + sel.Sel.Name
				if !allowed[name] {
					out = append(out, fmt.Sprintf("%s: %s bypasses the engine facade", pos, name))
				}
			}
		}
		if sel.Sel.Name == "Publish" && endsWithName(sel.X, "uiBus") {
			out = append(out, fmt.Sprintf("%s: uiBus.Publish bypasses the engine facade", pos))
		}
		if sel.Sel.Name == "Wiring" {
			out = append(out, fmt.Sprintf("%s: Wiring exposes the run internals", pos))
		}
		return true
	})
	return out
}

// endsWithName reports whether expr is the identifier name or a selector chain ending in it (s.uiBus).
func endsWithName(expr ast.Expr, name string) bool {
	switch e := expr.(type) {
	case *ast.ParenExpr:
		return endsWithName(e.X, name)
	case *ast.TypeAssertExpr:
		return endsWithName(e.X, name)
	case *ast.Ident:
		return e.Name == name
	case *ast.SelectorExpr:
		return e.Sel.Name == name
	}
	return false
}

// Every forbidden form must be caught, including nested selectors and aliased imports; the allowed
// operation must not hide a different one in the same file.
func TestBoundary_FacadeCheckCatchesBypasses(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		allowed map[string]bool
		want    int
	}{
		{"orchestrator type", `package p; import "github.com/akopichin/afm-engine/internal/orchestrator"; var _ *orchestrator.Orchestrator`, nil, 1},
		{"aliased orchestrator", `package p; import raworch "github.com/akopichin/afm-engine/internal/orchestrator"; var _ *raworch.Orchestrator`, nil, 1},
		{"orchestrator constructor", `package p; import "github.com/akopichin/afm-engine/internal/orchestrator"; var _ = orchestrator.New`, nil, 1},
		{"state open", `package p; import "github.com/akopichin/afm-engine/internal/state"; var _ = state.Open`, nil, 1},
		{"aliased state store", `package p; import st "github.com/akopichin/afm-engine/internal/state"; var _ st.Store`, nil, 1},
		{"dot import", `package p; import . "github.com/akopichin/afm-engine/internal/state"`, nil, 1},
		{"nested uiBus publish", `package p; func f(s struct{ uiBus interface{ Publish() } }) { s.uiBus.Publish() }`, nil, 1},
		{"bare uiBus publish", `package p; func f(uiBus interface{ Publish() }) { uiBus.Publish() }`, nil, 1},
		{"asserted uiBus publish", `package p; func f(s struct{ uiBus any }) { s.uiBus.(interface{ Publish() }).Publish() }`, nil, 1},
		{"parenthesized uiBus publish", `package p; func f(s struct{ uiBus interface{ Publish() } }) { (s.uiBus).Publish() }`, nil, 1},
		{"state pre-note", `package p; import "github.com/akopichin/afm-engine/internal/state"; var _ = state.SavePreNote`, nil, 1},
		{"mcp answer file", `package p; import "github.com/akopichin/afm/contracts/mcp"; var _ = mcp.WriteAnswerFile`, nil, 1},
		{"mcp append answer", `package p; import "github.com/akopichin/afm/contracts/mcp"; var _ = mcp.AppendAnswer`, nil, 1},
		{"stagefiles notice", `package p; import "github.com/akopichin/afm-engine/internal/orchestrator/stagefiles"; var _ = stagefiles.AppendNotice`, nil, 1},
		{"wiring call", `package p; func f(run interface{ Wiring() struct{ Store interface{ Close() } } }) { run.Wiring().Store.Close() }`, nil, 1},
		{"os write", `package p; import "os"; var _ = os.WriteFile`, nil, 1},
		{"aliased os create", `package p; import stdos "os"; var _ = stdos.Create`, nil, 1},
		{"os rename", `package p; import "os"; var _ = os.Rename`, nil, 1},
		{"allowed op does not hide another", `package p; import "os"; var _, _ = os.OpenFile, os.WriteFile`, map[string]bool{"os.OpenFile": true}, 1},
		{"allowed op passes", `package p; import "os"; var _ = os.OpenFile`, map[string]bool{"os.OpenFile": true}, 0},
		{"unrelated local named os", `package p; func f(os struct{ WriteFile int }) { _ = os.WriteFile }`, nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := facadeViolations(t, "fixture.go", []byte(c.src), c.allowed)
			if len(got) != c.want {
				t.Errorf("violations = %d %v, want %d", len(got), got, c.want)
			}
		})
	}
}
