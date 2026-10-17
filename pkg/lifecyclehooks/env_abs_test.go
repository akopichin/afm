package lifecyclehooks

import (
	"path/filepath"
	"testing"
)

// T6 (dev-plan.md, D4): AFM_RUN_DIR/AFM_ROOT_DIR в окружении hook-процесса пересекают границу процесса
// (cmd.Dir = root_dir для не-inherit хука) — поэтому абсолютизируются, в отличие от JSON payload на stdin,
// у которого свой контракт схемы и он не трогается.
func TestHookEnv_RunDirAndRootDirAreAbsolute(t *testing.T) {
	cfg := DispatcherConfig{
		FlowName: "f",
		RunID:    "r1",
		RunDir:   filepath.Join(".afm", "runs", "r1"),
		RootDir:  filepath.Join("project", "root"),
	}
	p := BuildPayload(cfg, Event{Type: EventFlowStarted}, "id")

	// Контракт схемы: JSON payload несёт исходные (относительные) значения, не абсолютные.
	if p.Flow.RunDir != cfg.RunDir {
		t.Fatalf("payload.Flow.RunDir = %q, want unchanged %q", p.Flow.RunDir, cfg.RunDir)
	}
	if p.Flow.RootDir != cfg.RootDir {
		t.Fatalf("payload.Flow.RootDir = %q, want unchanged %q", p.Flow.RootDir, cfg.RootDir)
	}

	env := buildEnv(Hook{ID: "h"}, cfg, p)

	wantRunDir, err := filepath.Abs(cfg.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	wantRootDir, err := filepath.Abs(cfg.RootDir)
	if err != nil {
		t.Fatal(err)
	}

	if !hasEnv(env, "AFM_RUN_DIR", wantRunDir) {
		t.Errorf("env missing AFM_RUN_DIR=%q; got %v", wantRunDir, env)
	}
	if !hasEnv(env, "AFM_ROOT_DIR", wantRootDir) {
		t.Errorf("env missing AFM_ROOT_DIR=%q; got %v", wantRootDir, env)
	}
}

// TestHookEnv_EmptyRootDirStaysEmpty закрепляет, что absEnvPath не превращает легитимно-пустой RootDir
// (наследование CWD afm) в CWD-путь: filepath.Abs("") вернул бы процессный CWD, что было бы сменой
// семантики "не задано" на конкретный путь.
func TestHookEnv_EmptyRootDirStaysEmpty(t *testing.T) {
	cfg := DispatcherConfig{FlowName: "f", RunID: "r1", RunDir: "runs/r1", RootDir: ""}
	p := BuildPayload(cfg, Event{Type: EventFlowStarted}, "id")

	env := buildEnv(Hook{ID: "h"}, cfg, p)
	if !hasEnv(env, "AFM_ROOT_DIR", "") {
		t.Errorf("env AFM_ROOT_DIR should stay empty for unset RootDir; got %v", env)
	}
}
