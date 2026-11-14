package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/flow"
)

const exportFlow = `# Release flow
name: release

stages:
  - id: build   # compile everything
    agents: [auto]
    command: claude

    assistants:
      - profile: reviewer
        title: Reviewer
      - profile: tester

  - id: deploy
    script: ship
team:
  - id: lead
    profile: reviewer
    title: Lead
`

var (
	stageRef  = budget.DeclarationRef{Kind: budget.DeclStage, StageID: "build"}
	helperRef = budget.DeclarationRef{Kind: budget.DeclStageAssistant, StageID: "build", Profile: "reviewer"}
	teamRef   = budget.DeclarationRef{Kind: budget.DeclTeamMember, MemberID: "lead"}
	flowRef   = budget.DeclarationRef{Kind: budget.DeclFlow}
	limit     = budget.Spec{Tokens: 500_000, MoneyUSD: 2.5, StopPolicy: budget.PolicyMedium}
)

// exportFixture is a flow file in a temp afm-root and an exporter that knows its parsed budgets.
type exportFixture struct {
	t    *testing.T
	root string
	path string
	exp  *budgetExporter
}

func newExportFixture(t *testing.T, body string) *exportFixture {
	t.Helper()
	root := t.TempDir()
	fx := &exportFixture{t: t, root: root, path: filepath.Join(root, "flow.yaml")}
	fx.write(body)
	fx.exp = newBudgetExporter(fx.path, root, fx.parse())
	return fx
}

func (fx *exportFixture) write(body string) {
	fx.t.Helper()
	if err := os.WriteFile(fx.path, []byte(body), 0o644); err != nil {
		fx.t.Fatal(err)
	}
}

func (fx *exportFixture) read() string {
	fx.t.Helper()
	data, err := os.ReadFile(fx.path)
	if err != nil {
		fx.t.Fatal(err)
	}
	return string(data)
}

func (fx *exportFixture) parse() *flow.Flow {
	fx.t.Helper()
	f, err := flow.ParseFile(fx.path)
	if err != nil {
		fx.t.Fatal(err)
	}
	return f
}

func (fx *exportFixture) export(ref budget.DeclarationRef, spec budget.Spec) budget.ExportResult {
	return fx.exp.Export(context.Background(), ref, spec)
}

func requireSaved(t *testing.T, res budget.ExportResult) {
	t.Helper()
	if res.Status != budget.ExportSaved || res.Path != "flow.yaml" || res.Error != "" {
		t.Fatalf("result = %+v, want saved flow.yaml", res)
	}
}

func TestBudgetExport_InsertsBlockRightAfterTheIdentifyingKey(t *testing.T) {
	cases := []struct {
		name string
		ref  budget.DeclarationRef
		want string
	}{
		{"stage", stageRef, strings.Replace(exportFlow, "  - id: build   # compile everything\n",
			"  - id: build   # compile everything\n    budget:\n      tokens: 500000\n      money: 2.5\n      stop_policy: medium\n", 1)},
		{"stage assistant", helperRef, strings.Replace(exportFlow, "      - profile: reviewer\n        title: Reviewer\n",
			"      - profile: reviewer\n        budget:\n          tokens: 500000\n          money: 2.5\n          stop_policy: medium\n        title: Reviewer\n", 1)},
		{"team member", teamRef, strings.Replace(exportFlow, "  - id: lead\n    profile: reviewer\n",
			"  - id: lead\n    budget:\n      tokens: 500000\n      money: 2.5\n      stop_policy: medium\n    profile: reviewer\n", 1)},
		{"flow", flowRef, exportFlow + "budget:\n  tokens: 500000\n  money: 2.5\n  stop_policy: medium\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newExportFixture(t, exportFlow)
			requireSaved(t, fx.export(tc.ref, limit))
			if got := fx.read(); got != tc.want {
				t.Fatalf("file:\n%s\nwant:\n%s", got, tc.want)
			}
			if got, _ := declaredBudget(fx.parse(), tc.ref); got == nil || *got != limit {
				t.Errorf("parsed budget = %+v, want %+v", got, limit)
			}
		})
	}
}

func TestBudgetExport_ReplacesBlockKeepingKeyLineAndNeighbours(t *testing.T) {
	body := strings.Replace(exportFlow, "  - id: build   # compile everything\n",
		"  - id: build   # compile everything\n    budget:   # monthly cap\n      tokens: 1m\n      stop_policy: info\n", 1)
	fx := newExportFixture(t, body)
	requireSaved(t, fx.export(stageRef, limit))

	want := strings.Replace(exportFlow, "  - id: build   # compile everything\n",
		"  - id: build   # compile everything\n    budget:   # monthly cap\n      tokens: 500000\n      money: 2.5\n      stop_policy: medium\n", 1)
	if got := fx.read(); got != want {
		t.Fatalf("file:\n%s\nwant:\n%s", got, want)
	}
}

func TestBudgetExport_ReplacesInlineValue(t *testing.T) {
	body := strings.Replace(exportFlow, "    agents: [auto]\n    command: claude\n", "    agents: [auto]\n    command: claude\n    budget: {tokens: 1m, stop_policy: info}  # cap\n", 1)
	fx := newExportFixture(t, body)
	requireSaved(t, fx.export(stageRef, limit))

	want := strings.Replace(exportFlow, "    agents: [auto]\n    command: claude\n", "    agents: [auto]\n    command: claude\n    budget:  # cap\n      tokens: 500000\n      money: 2.5\n      stop_policy: medium\n", 1)
	if got := fx.read(); got != want {
		t.Fatalf("file:\n%s\nwant:\n%s", got, want)
	}
}

func TestBudgetExport_RootBlockReplacedInPlace(t *testing.T) {
	body := "name: release\nbudget:\n  tokens: 100k\nstages:\n  - id: build\n    agents: [auto]\n    command: claude\n"
	fx := newExportFixture(t, body)
	requireSaved(t, fx.export(flowRef, limit))
	want := "name: release\nbudget:\n  tokens: 500000\n  money: 2.5\n  stop_policy: medium\nstages:\n  - id: build\n    agents: [auto]\n    command: claude\n"
	if got := fx.read(); got != want {
		t.Fatalf("file:\n%s\nwant:\n%s", got, want)
	}
}

func TestBudgetExport_KeepsCRLFAndMissingFinalNewline(t *testing.T) {
	crlf := strings.ReplaceAll(exportFlow, "\n", "\r\n")
	fx := newExportFixture(t, crlf)
	requireSaved(t, fx.export(stageRef, limit))
	got := fx.read()
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Errorf("a bare LF was written into a CRLF file: %q", got)
	}
	if !strings.Contains(got, "  - id: build   # compile everything\r\n    budget:\r\n      tokens: 500000\r\n") {
		t.Errorf("block not inserted with CRLF:\n%q", got)
	}

	noFinal := strings.TrimSuffix(exportFlow, "\n")
	fx = newExportFixture(t, noFinal)
	requireSaved(t, fx.export(flowRef, limit))
	if got := fx.read(); !strings.HasSuffix(got, "stop_policy: medium") || strings.HasSuffix(got, "\n") {
		t.Errorf("final newline appeared:\n%q", got)
	}

	fx = newExportFixture(t, noFinal)
	requireSaved(t, fx.export(teamRef, limit))
	if got := fx.read(); strings.HasSuffix(got, "\n") || !strings.Contains(got, "  - id: lead\n    budget:\n") {
		t.Errorf("team export wrong:\n%q", got)
	}
}

func TestBudgetExport_FreshnessIsPerDeclaration(t *testing.T) {
	fx := newExportFixture(t, exportFlow)

	// Another place of the file changes: the export still goes through.
	fx.write(strings.Replace(exportFlow, "script: ship", "script: ship --fast", 1))
	requireSaved(t, fx.export(stageRef, limit))

	// The same declaration changes outside the run: refused, the file is untouched.
	edited := strings.Replace(fx.read(), "tokens: 500000", "tokens: 7", 1)
	fx.write(edited)
	res := fx.export(stageRef, budget.Spec{Tokens: 9, StopPolicy: budget.PolicyHard})
	if res.Status != budget.ExportFailed || !strings.Contains(res.Error, "changed outside this run") {
		t.Fatalf("result = %+v, want failed: changed outside this run", res)
	}
	if fx.read() != edited {
		t.Error("a refused export touched the file")
	}
}

func TestBudgetExport_RememberedExportIsTheNewBase(t *testing.T) {
	fx := newExportFixture(t, exportFlow)
	requireSaved(t, fx.export(stageRef, limit))
	// The file now holds the previous export, which this run knows about, so a second one is fresh.
	requireSaved(t, fx.export(stageRef, budget.Spec{Tokens: 42, StopPolicy: budget.PolicyHard}))
	if got, _ := declaredBudget(fx.parse(), stageRef); got == nil || got.Tokens != 42 {
		t.Errorf("parsed budget = %+v", got)
	}
}

func TestBudgetExport_Refusals(t *testing.T) {
	cases := []struct {
		name string
		body string
		ref  budget.DeclarationRef
		want string
	}{
		{"anchor on the stage", "name: r\nstages:\n  - &s\n    id: build\n    agents: [auto]\n    command: claude\n", stageRef, "anchors, aliases or merge keys"},
		{"merge key", "x-base: &base\n  agents: [auto]\n  command: claude\nname: r\nstages:\n  - id: build\n    <<: *base\n", stageRef, "anchors, aliases or merge keys"},
		{"inline stage", "name: r\nstages:\n  - {id: build, agents: [auto], command: claude}\n", stageRef, "inline"},
		{"helper by name only", "name: r\nstages:\n  - id: build\n    agents: [auto]\n    command: claude\n    assistants: reviewer\n", helperRef, "profile name only"},
		{"helper by name in a list", "name: r\nstages:\n  - id: build\n    agents: [auto]\n    command: claude\n    assistants:\n      - reviewer\n", helperRef, "profile name only"},
		{"missing stage", exportFlow, budget.DeclarationRef{Kind: budget.DeclStage, StageID: "dynamic"}, "does not declare"},
		{"missing member", exportFlow, budget.DeclarationRef{Kind: budget.DeclTeamMember, MemberID: "nobody"}, "does not declare"},
		{"multi-line block", "name: r\nstages:\n  - id: build\n    agents: [auto]\n    command: claude\n    budget:\n      tokens: 100k\n      # why\n      stop_policy: info\n", stageRef, "comments"},
		{"column-0 comment inside the block", "name: r\nstages:\n  - id: build\n    agents: [auto]\n    command: claude\n    budget:\n      tokens: 100k\n# why\n      stop_policy: info\n", stageRef, "comments"},
		{"commented child", "name: r\nstages:\n  - id: build\n    agents: [auto]\n    command: claude\n    budget:\n      tokens: 100k  # why\n", stageRef, "comments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newExportFixture(t, tc.body)
			res := fx.export(tc.ref, limit)
			if res.Status != budget.ExportUnavailable || !strings.Contains(res.Error, tc.want) {
				t.Fatalf("result = %+v, want unavailable: %s", res, tc.want)
			}
			if fx.read() != tc.body {
				t.Error("a refused export touched the file")
			}
			if _, reason, _ := fx.exp.Check(tc.ref); !strings.Contains(reason, tc.want) {
				t.Errorf("Check reason = %q, want %q", reason, tc.want)
			}
		})
	}
}

func TestBudgetExport_FileSystemRefusals(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		fx := newExportFixture(t, exportFlow)
		target := filepath.Join(fx.root, "target.yaml")
		if err := os.Rename(fx.path, target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, fx.path); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
		res := fx.export(stageRef, limit)
		if res.Status != budget.ExportFailed || res.Error != "flow file is a symbolic link" {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("read-only file", func(t *testing.T) {
		fx := newExportFixture(t, exportFlow)
		if err := os.Chmod(fx.path, 0o444); err != nil {
			t.Fatal(err)
		}
		skipIfRoot(t)
		res := fx.export(stageRef, limit)
		if res.Status != budget.ExportFailed || !strings.Contains(res.Error, "not writable") {
			t.Fatalf("result = %+v", res)
		}
		if fx.read() != exportFlow {
			t.Error("file changed")
		}
	})
	t.Run("read-only directory", func(t *testing.T) {
		fx := newExportFixture(t, exportFlow)
		if err := os.Chmod(fx.root, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(fx.root, 0o755) })
		skipIfRoot(t)
		res := fx.export(stageRef, limit)
		if res.Status != budget.ExportFailed || !strings.Contains(res.Error, "not writable") {
			t.Fatalf("result = %+v", res)
		}
	})
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
}

func TestBudgetExport_ErrorsNeverCarryAbsolutePaths(t *testing.T) {
	fx := newExportFixture(t, exportFlow)
	fx.write("name: [broken")
	res := fx.export(stageRef, limit)
	if res.Status != budget.ExportFailed || strings.Contains(res.Error, fx.root) || strings.Contains(res.Path, fx.root) {
		t.Fatalf("result = %+v leaks the root %s", res, fx.root)
	}
}

func TestBudgetExport_CheckReportsDeclaredBudgetWithoutWriting(t *testing.T) {
	body := strings.Replace(exportFlow, "    agents: [auto]\n    command: claude\n", "    agents: [auto]\n    command: claude\n    budget:\n      tokens: 100k\n", 1)
	fx := newExportFixture(t, body)
	file, reason, declared := fx.exp.Check(stageRef)
	if file != "flow.yaml" || reason != "" || declared == nil || declared.Tokens != 100_000 {
		t.Fatalf("Check = %q %q %+v", file, reason, declared)
	}
	if fx.read() != body {
		t.Error("Check wrote the file")
	}
}

func TestBudgetExport_UnchangedFlowSemanticsCheck(t *testing.T) {
	// The semantic check rejects a rewrite that moved anything besides the budget.
	before := []byte(exportFlow)
	after := []byte(strings.Replace(exportFlow, "script: ship", "script: nope", 1))
	if rewriteKeepsFlow(before, after, stageRef, limit) {
		t.Fatal("a changed script passed the check")
	}
	bad := []byte(strings.Replace(exportFlow, "  - id: build   # compile everything\n", "  - id: build\n    budget:\n      tokens: 1\n", 1))
	if rewriteKeepsFlow(before, bad, stageRef, limit) {
		t.Fatal("a different budget passed the check")
	}
}

func TestBudgetExport_FileModeIsKept(t *testing.T) {
	fx := newExportFixture(t, exportFlow)
	if err := os.Chmod(fx.path, 0o640); err != nil {
		t.Fatal(err)
	}
	requireSaved(t, fx.export(stageRef, limit))
	info, err := os.Stat(fx.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", info.Mode().Perm())
	}
	if entries, _ := filepath.Glob(filepath.Join(fx.root, ".afm-flow-*")); len(entries) != 0 {
		t.Errorf("temp files left: %v", entries)
	}
}

// An edit that lands somewhere the flow parser does not read (a second YAML document) fails the result check
// and leaves the file alone.
func TestBudgetExport_FailedResultCheckLeavesFileUntouched(t *testing.T) {
	body := exportFlow + "---\nother: 1\n"
	fx := newExportFixture(t, body)
	res := fx.export(flowRef, limit)
	if res.Status != budget.ExportFailed || !strings.Contains(res.Error, "did not pass the check") {
		t.Fatalf("result = %+v", res)
	}
	if fx.read() != body {
		t.Error("the file changed although the check failed")
	}
}

func TestBudgetExport_InvalidFlowFileIsNotSaved(t *testing.T) {
	fx := newExportFixture(t, exportFlow)
	broken := strings.Replace(exportFlow, "  - id: deploy\n", "  - id: deploy\n    depends_on: [nowhere]\n", 1)
	fx.write(broken)
	res := fx.export(stageRef, limit)
	if res.Status != budget.ExportFailed || !strings.Contains(res.Error, "did not pass the check") {
		t.Fatalf("result = %+v", res)
	}
	if fx.read() != broken {
		t.Error("the file changed although the flow does not validate")
	}
}

// The path checks run again right before the swap: the file may be replaced after it was read.
func TestBudgetExport_CommitRevalidatesThePath(t *testing.T) {
	prepare := func(t *testing.T) (*exportFixture, []byte, os.FileInfo) {
		fx := newExportFixture(t, exportFlow)
		data, info, reason := fx.exp.readFile()
		if reason != "" {
			t.Fatal(reason)
		}
		return fx, data, info
	}
	rewritten := func(fx *exportFixture, data []byte) []byte {
		out, reason := rewriteBudgetBlock(data, declarationNode{mapping: mustRoot(t, data)}, []string{"tokens: 1"})
		if reason != "" {
			t.Fatal(reason)
		}
		return out
	}
	t.Run("another file took the path", func(t *testing.T) {
		fx, data, info := prepare(t)
		other := filepath.Join(fx.root, "other.yaml")
		if err := os.WriteFile(other, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(other, fx.path); err != nil {
			t.Fatal(err)
		}
		if got := fx.exp.replaceFile(data, rewritten(fx, data), info); got != reasonChanged {
			t.Errorf("reason = %q, want %q", got, reasonChanged)
		}
		if fx.read() != string(data) {
			t.Error("the file changed")
		}
	})
	t.Run("the path became a symlink", func(t *testing.T) {
		fx, data, info := prepare(t)
		target := filepath.Join(fx.root, "target.yaml")
		if err := os.Rename(fx.path, target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, fx.path); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
		if got := fx.exp.replaceFile(data, rewritten(fx, data), info); got != reasonSymlink {
			t.Errorf("reason = %q, want %q", got, reasonSymlink)
		}
	})
	t.Run("write access was lost", func(t *testing.T) {
		fx, data, info := prepare(t)
		if err := os.Chmod(fx.path, 0o444); err != nil {
			t.Fatal(err)
		}
		skipIfRoot(t)
		if got := fx.exp.replaceFile(data, rewritten(fx, data), info); got != reasonNotWritable {
			t.Errorf("reason = %q, want %q", got, reasonNotWritable)
		}
		if fx.read() != string(data) {
			t.Error("the file changed")
		}
	})
}

func mustRoot(t *testing.T, data []byte) *yaml.Node {
	t.Helper()
	_, root, reason := decodeFlowYAML(data)
	if reason != "" {
		t.Fatal(reason)
	}
	return root
}
