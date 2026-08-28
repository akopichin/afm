package orchestrator

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memory"
)

// TestMemoryBlockForStage_DisabledReturnsEmpty — память выключена
// (MemoryProjectPath == ""), memoryBlockForStage не должен ничего вернуть,
// даже если Memory-конфиг задан.
func TestMemoryBlockForStage_DisabledReturnsEmpty(t *testing.T) {
	o := newTestOrchestrator(t)
	o.opts.MemoryProjectPath = ""
	o.opts.MemorySessionPath = ""

	got := o.memoryBlockForStage(flow.Stage{ID: "s1", Name: "Stage", Description: "do thing"})
	if got != "" {
		t.Errorf("disabled memory must return \"\", got %q", got)
	}
}

// TestMemoryBlockForStage_EmptyStoresReturnsEmpty — память включена, но оба
// стора пусты (первая стадия первого рана — файлов ещё нет): сказать агенту
// нечего, блок должен остаться пустым, а не показывать пустой pointer.
func TestMemoryBlockForStage_EmptyStoresReturnsEmpty(t *testing.T) {
	o := newTestOrchestrator(t)
	dir := t.TempDir()
	o.opts.MemoryProjectPath = filepath.Join(dir, "PROJECT_MEMORY.yaml")
	o.opts.MemorySessionPath = filepath.Join(dir, "SESSION_MEMORY.yaml")
	o.opts.Memory = flow.MemoryConfig{RetrievalThreshold: 25, CoreConfirmCount: 3}

	got := o.memoryBlockForStage(flow.Stage{ID: "s1", Name: "Stage", Description: "do thing"})
	if got != "" {
		t.Errorf("empty stores must return \"\", got %q", got)
	}
}

// TestMemoryBlockForStage_SmallStoreReturnsPointer — total findings <=
// threshold → Select signals injectAll; memoryBlockForStage must return a
// pointer block naming BOTH absolute paths instead of inlining content.
func TestMemoryBlockForStage_SmallStoreReturnsPointer(t *testing.T) {
	o := newTestOrchestrator(t)
	dir := t.TempDir()
	projPath := filepath.Join(dir, "PROJECT_MEMORY.yaml")
	sessPath := filepath.Join(dir, "SESSION_MEMORY.yaml")
	o.opts.MemoryProjectPath = projPath
	o.opts.MemorySessionPath = sessPath
	o.opts.Memory = flow.MemoryConfig{RetrievalThreshold: 25, CoreConfirmCount: 3}

	if err := memory.Save(projPath, memory.Store{Findings: []memory.Finding{
		{ID: "f1", Scope: memory.ScopeProject, Kind: memory.KindFact, Statement: "uses sqlite", Evidence: "e:1", ConfirmCount: 1},
	}}); err != nil {
		t.Fatal(err)
	}

	got := o.memoryBlockForStage(flow.Stage{ID: "s1", Name: "Stage", Description: "do thing"})
	if got == "" {
		t.Fatal("small store must return a non-empty pointer block")
	}
	if !strings.Contains(got, projPath) {
		t.Errorf("pointer block must name project path %q:\n%s", projPath, got)
	}
	if !strings.Contains(got, sessPath) {
		t.Errorf("pointer block must name session path %q:\n%s", sessPath, got)
	}
	if strings.Contains(got, "uses sqlite") {
		t.Errorf("small-store pointer block must NOT inline finding content:\n%s", got)
	}
}

// TestMemoryBlockForStage_LargeStoreInlinesRender — total findings > threshold
// → Select filters; memoryBlockForStage must inline memory.Render(sel),
// containing a core/relevant finding's statement but NOT an irrelevant
// low-confidence one, plus a trailing pointer line to the project path.
func TestMemoryBlockForStage_LargeStoreInlinesRender(t *testing.T) {
	o := newTestOrchestrator(t)
	dir := t.TempDir()
	projPath := filepath.Join(dir, "PROJECT_MEMORY.yaml")
	sessPath := filepath.Join(dir, "SESSION_MEMORY.yaml")
	o.opts.MemoryProjectPath = projPath
	o.opts.MemorySessionPath = sessPath
	o.opts.Memory = flow.MemoryConfig{RetrievalThreshold: 25, CoreConfirmCount: 3}

	var findings []memory.Finding
	for i := 0; i < 30; i++ {
		findings = append(findings, memory.Finding{
			ID:           "irrelevant-" + string(rune('a'+i)),
			Scope:        memory.ScopeProject,
			Kind:         memory.KindFact,
			Topic:        []string{"misc"},
			Statement:    "irrelevant low-confidence thing",
			Evidence:     "e:1",
			ConfirmCount: 1,
		})
	}
	findings = append(findings, memory.Finding{
		ID:           "core1",
		Scope:        memory.ScopeProject,
		Kind:         memory.KindFact,
		Topic:        []string{"database"},
		Statement:    "core high-confidence rule about the database schema",
		Evidence:     "e:2",
		ConfirmCount: 5,
	})
	if err := memory.Save(projPath, memory.Store{Findings: findings}); err != nil {
		t.Fatal(err)
	}

	got := o.memoryBlockForStage(flow.Stage{ID: "database-migration", Name: "Database migration", Description: "migrate the database schema"})
	if got == "" {
		t.Fatal("large store must return a non-empty inlined block")
	}
	if !strings.Contains(got, "core high-confidence rule about the database schema") {
		t.Errorf("inlined block must contain the core/relevant finding:\n%s", got)
	}
	if strings.Contains(got, "irrelevant low-confidence thing") {
		t.Errorf("inlined block must NOT contain the irrelevant low-confidence finding:\n%s", got)
	}
	if !strings.Contains(got, projPath) {
		t.Errorf("inlined block must still point at the project memory path %q:\n%s", projPath, got)
	}
}
