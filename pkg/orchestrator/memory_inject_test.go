package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memory"
)

// TestMemoryBlockForStage_DisabledReturnsEmpty — память выключена
// (MemoryDir == ""), memoryBlockForStage не должен ничего вернуть.
func TestMemoryBlockForStage_DisabledReturnsEmpty(t *testing.T) {
	o := newTestOrchestrator(t)
	o.opts.MemoryDir = ""

	got := o.memoryBlockForStage(flow.Stage{ID: "s1", Name: "Stage", Description: "do thing"})
	if got != "" {
		t.Errorf("disabled memory must return \"\", got %q", got)
	}
}

// TestMemoryBlockForStage_NoFilesReturnsEmpty — память включена, но ни
// project-, ни stage-файла ещё нет на диске (первая стадия первого рана) —
// сказать агенту нечего, блок должен остаться пустым.
func TestMemoryBlockForStage_NoFilesReturnsEmpty(t *testing.T) {
	o := newTestOrchestrator(t)
	o.opts.MemoryDir = t.TempDir()
	o.opts.Memory = flow.MemoryConfig{Mode: flow.ReflectModeRW, MemoryUse: true}

	got := o.memoryBlockForStage(flow.Stage{ID: "s1", Name: "Stage", Description: "do thing"})
	if got != "" {
		t.Errorf("no files on disk must return \"\", got %q", got)
	}
}

// TestMemoryBlockForStage_ProjectFileNamedForEveryStage — memory.md существует
// → указатель на него должен попасть в блок ЛЮБОЙ стадии, даже без reflect.
func TestMemoryBlockForStage_ProjectFileNamedForEveryStage(t *testing.T) {
	o := newTestOrchestrator(t)
	dir := t.TempDir()
	o.opts.MemoryDir = dir
	o.opts.Memory = flow.MemoryConfig{Mode: flow.ReflectModeRW, MemoryUse: true}
	projPath := memory.ProjectFile(dir)
	if err := os.WriteFile(projPath, []byte("# Project rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := o.memoryBlockForStage(flow.Stage{ID: "s1", Name: "Stage", Description: "do thing"})
	if got == "" {
		t.Fatal("existing project memory must produce a non-empty pointer block")
	}
	if !strings.Contains(got, projPath) {
		t.Errorf("pointer block must name project path %q:\n%s", projPath, got)
	}
}

// TestMemoryBlockForStage_ReadableStageFileNamed — stage reflect mode r/rw и
// файл существует → его путь тоже должен попасть в блок.
func TestMemoryBlockForStage_ReadableStageFileNamed(t *testing.T) {
	o := newTestOrchestrator(t)
	dir := t.TempDir()
	o.opts.MemoryDir = dir
	o.opts.Memory = flow.MemoryConfig{Mode: flow.ReflectModeRW, MemoryUse: true}
	stagePath := memory.StageFile(dir, "s1.md")
	if err := os.WriteFile(stagePath, []byte("# Project rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stage := flow.Stage{ID: "s1", Name: "Stage", Reflect: &flow.Reflect{File: "s1.md", Mode: flow.ReflectModeR}}
	got := o.memoryBlockForStage(stage)
	if got == "" {
		t.Fatal("existing readable stage file must produce a non-empty pointer block")
	}
	if !strings.Contains(got, stagePath) {
		t.Errorf("pointer block must name stage path %q:\n%s", stagePath, got)
	}
}

// TestMemoryBlockForStage_WriteOnlyModeDoesNotNameStageFile — mode:w — стадия
// пишет свой файл, но не читает его в собственном промпте.
func TestMemoryBlockForStage_WriteOnlyModeDoesNotNameStageFile(t *testing.T) {
	o := newTestOrchestrator(t)
	dir := t.TempDir()
	o.opts.MemoryDir = dir
	o.opts.Memory = flow.MemoryConfig{Mode: flow.ReflectModeRW, MemoryUse: true}
	stagePath := memory.StageFile(dir, "s1.md")
	if err := os.WriteFile(stagePath, []byte("# Project rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stage := flow.Stage{ID: "s1", Name: "Stage", Reflect: &flow.Reflect{File: "s1.md", Mode: flow.ReflectModeW}}
	got := o.memoryBlockForStage(stage)
	if strings.Contains(got, stagePath) {
		t.Errorf("write-only mode must NOT name the stage file in its own prompt:\n%s", got)
	}
}

// TestMemoryBlockForStage_NonexistentStageFileNotNamed — mode:rw, но файл ещё
// не создан на диске (первый запуск): не называть путь, которого нет смысла
// читать.
func TestMemoryBlockForStage_NonexistentStageFileNotNamed(t *testing.T) {
	o := newTestOrchestrator(t)
	dir := t.TempDir()
	o.opts.MemoryDir = dir
	o.opts.Memory = flow.MemoryConfig{Mode: flow.ReflectModeRW, MemoryUse: true}

	stage := flow.Stage{ID: "s1", Name: "Stage", Reflect: &flow.Reflect{File: "s1.md", Mode: flow.ReflectModeRW}}
	got := o.memoryBlockForStage(stage)
	if strings.Contains(got, filepath.Join(dir, "s1.md")) {
		t.Errorf("nonexistent stage file must not be named:\n%s", got)
	}
}

// TestMemoryBlockForStage_ParticipationOff — глобальный memory_use=false и стадия
// не переопределяет → ничего не подмешивается, даже если memory.md существует.
func TestMemoryBlockForStage_ParticipationOff(t *testing.T) {
	o := newTestOrchestrator(t)
	dir := t.TempDir()
	o.opts.MemoryDir = dir
	o.opts.Memory = flow.MemoryConfig{Mode: flow.ReflectModeRW, MemoryUse: false}
	if err := os.WriteFile(memory.ProjectFile(dir), []byte("# Project rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := o.memoryBlockForStage(flow.Stage{ID: "s1", Name: "Stage"}); got != "" {
		t.Errorf("memory_use off must inject nothing, got:\n%s", got)
	}
}

// TestMemoryBlockForStage_StageOverrideOptsIn — глобально memory_use=false, но
// стадия задаёт memory_use=true → память для этой стадии подмешивается.
func TestMemoryBlockForStage_StageOverrideOptsIn(t *testing.T) {
	o := newTestOrchestrator(t)
	dir := t.TempDir()
	o.opts.MemoryDir = dir
	o.opts.Memory = flow.MemoryConfig{Mode: flow.ReflectModeRW, MemoryUse: false}
	projPath := memory.ProjectFile(dir)
	if err := os.WriteFile(projPath, []byte("# Project rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	on := true
	got := o.memoryBlockForStage(flow.Stage{ID: "s1", Name: "Stage", MemoryUse: &on})
	if !strings.Contains(got, projPath) {
		t.Errorf("stage memory_use=true override must inject memory.md, got:\n%s", got)
	}
}

// TestMemoryBlockForStage_GlobalModeWriteOnlyHidesProject — memory.mode "w"
// (write-only global): memory.md не подмешивается, даже если участие включено.
func TestMemoryBlockForStage_GlobalModeWriteOnlyHidesProject(t *testing.T) {
	o := newTestOrchestrator(t)
	dir := t.TempDir()
	o.opts.MemoryDir = dir
	o.opts.Memory = flow.MemoryConfig{Mode: flow.ReflectModeW, MemoryUse: true}
	projPath := memory.ProjectFile(dir)
	if err := os.WriteFile(projPath, []byte("# Project rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := o.memoryBlockForStage(flow.Stage{ID: "s1", Name: "Stage"}); strings.Contains(got, projPath) {
		t.Errorf("write-only global memory must NOT inject memory.md, got:\n%s", got)
	}
}
