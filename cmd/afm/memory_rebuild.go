package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memory"
	"github.com/akopichin/afm/pkg/memorypipeline"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// rebuildOptions — параметры `afm memory rebuild`, собранные из флагов
// команды. CommitSet отличает "флаг --commit/--no-commit реально передан" от
// "не передан вовсе" — Commit сам по себе (bool) этого не умеет: значение
// по умолчанию false неотличимо от явного --no-commit (review #12).
type rebuildOptions struct {
	FlowArg, RunID       string
	DryRun, ForceReflect bool
	CommitSet, Commit    bool
}

// newRebuildPipeline — seam over memorypipeline.New: тесты подменяют её на
// memorypipeline.New(p, a, memorypipeline.WithRunner(stub)), чтобы не
// запускать реального агента.
var newRebuildPipeline = func(p memorypipeline.Prompts, a memorypipeline.AgentConfig) *memorypipeline.Pipeline {
	return memorypipeline.New(p, a)
}

// rebuildHandler — единственная точка входа в реализацию `afm memory
// rebuild`: preflight (конфиг флоу, доступность цели, целостность
// завершённого рана), последовательность локов (run-лок сначала, лок памяти
// только на время Finalize), двухфазный запуск (CaptureAll без лока памяти,
// затем AcquireMemoryLock + Finalize), и жизненный цикл манифеста попытки
// (running -> ... -> completed/failed/cancelled). Var, а не func — тесты
// подменяют её целиком в самых верхнеуровневых тестах (напр. проверке
// регистрации команды); сама реализация использует seam newRebuildPipeline.
var rebuildHandler = func(ctx context.Context, o rebuildOptions) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	home, _ := os.UserHomeDir()
	cfg, err := config.LoadFrom(filepath.Join(home, config.AfmDir), fmDir())
	if err != nil {
		return err
	}

	var flowArgs []string
	if o.FlowArg != "" {
		flowArgs = []string{o.FlowArg}
	} // nil -> resolveFlowPath сканирует flowsDir()
	flowPath, err := resolveFlowPath(flowArgs)
	if err != nil {
		return err
	}
	f, err := flow.ParseFile(flowPath)
	if err != nil {
		return err
	}
	if !f.MemoryEnabled() {
		return fmt.Errorf("flow %q has no memory.path — nothing to build", f.Name)
	}
	if !hasWritableTarget(f) {
		return errors.New("no writable memory target (need a stage with reflect.mode w|rw)")
	}

	// rebuild нужен стабильный АБСОЛЮТНЫЙ корень (манифест, Executor.Dir "с
	// нуля", лок памяти) — в отличие от `afm run`, который может отдать
	// агентам унаследованный CWD процесса при пустом root_dir. Вычисляется
	// ДО резолва runDir — обе величины зависят только от f/rootDir, а Docker
	// re-exec ниже нужны все три (agentRoot/memDir/runDir) ДО захвата лока.
	agentRoot, err := absoluteAgentRoot(rootDir, f)
	if err != nil {
		return err
	}
	memDir, err := resolveMemoryDir(rootDir, agentRoot, f)
	if err != nil {
		return err
	}

	stageIDs := stageIDsOf(f)
	var runDir string
	if o.RunID != "" {
		runDir, err = resolveExplicitRunDir(runsDir(), f.Name, o.RunID)
	} else {
		runDir, err = state.FindLatestCompletedRunDir(runsDir(), f.Name, stageIDs)
	}
	if err != nil {
		return err
	}

	// Docker self-re-exec: если включён Docker-режим и мы не внутри
	// контейнера — перезапускаем себя в Docker ДО захвата любого лока (лок
	// должен быть взят внутри контейнера, тем же процессом, что его
	// реально держит). На успехе ReExec либо не возвращает управление вовсе,
	// либо возвращает *docker.SubprocessExitError — пробрасываем как есть,
	// main.go уже умеет превращать её в правильный os.Exit.
	absFlowPath, err := filepath.Abs(flowPath)
	if err != nil {
		return fmt.Errorf("resolve flow path: %w", err)
	}
	if err := rebuildDockerReExec(cfg, o, absFlowPath, agentRoot, memDir, runDir); err != nil {
		return err
	}

	runLock, err := state.TryLockRun(runDir)
	if err != nil {
		if errors.Is(err, state.ErrRunLocked) {
			return fmt.Errorf("run %s is active; stop `afm run` or wait", filepath.Base(runDir))
		}
		return err
	}
	defer runLock.Close() //nolint:errcheck

	rs, err := state.LoadRunState(runDir)
	if err != nil {
		return err
	}
	// Явный --run не фильтруется резолвером завершённых ранов, поэтому
	// полнота (AllDone) и точное множество стадий (checkStageSetMatchesExact)
	// проверяются НЕЗАВИСИМО — это ортогональные условия.
	if !rs.AllDone() {
		return fmt.Errorf("run %s is not completed (some stages are not done)", filepath.Base(runDir))
	}
	if err := checkStageSetMatchesExact(rs, stageIDs); err != nil {
		return err
	}

	// Fail-fast commit preflight ДО дорогостоящего capture, когда коммит
	// включён — не тратить время конвейера, если коммит заведомо невозможен.
	commit := effectiveCommit(o, f)
	if commit {
		if err := commitPreflight(memDir); err != nil {
			return err
		}
	}

	if warns := memorypipeline.ScanUnfinishedPromotions(runDir); len(warns) > 0 {
		fmt.Printf("warning: previous rebuild left an unfinished promotion in %v; re-running rebuilds cleanly\n", warns)
	}

	prompts, err := loadPrompts(cfg.PromptsDir)
	if err != nil {
		return err
	}

	// Единый wrapper-dir: generated-врапперы (autoShim) существуют только
	// ВНУТРИ контейнера — зеркалит блок run.go (cmd/afm/run.go). rebuild не
	// выполняет стадий флоу, поэтому единственная релевантная команда —
	// глобальный cfg.Client.Command (UsedRecipeCommands(nil, ...) — Task 13).
	var wrapperSpecs []docker.WrapperSpec
	generatedAgents := map[string]bool{}
	if os.Getenv("AFM_IN_DOCKER") == "1" && cfg.Docker.IsAutoShim() {
		if err := cfg.Docker.ValidateAgents(); err != nil {
			return err
		}
		used := docker.UsedRecipeCommands(nil, cfg.Client.Command, cfg.Docker.Agents)
		for cmd := range used {
			generatedAgents[cmd] = true
			wrapperSpecs = append(wrapperSpecs, buildWrapperSpec(cmd, cfg.Docker.Agents[cmd], cfg.Client.IsClaudeBare()))
		}
	}
	var wrapperDir string
	if len(wrapperSpecs) > 0 {
		wd, err := docker.CreateWrappers(wrapperSpecs)
		if err != nil {
			return fmt.Errorf("create wrappers: %w", err)
		}
		wrapperDir = wd
		defer os.RemoveAll(wd) //nolint:errcheck
	}

	agentCfg := memorypipeline.AgentConfig{
		Command:     cfg.Client.Command,
		ExtraArgs:   cfg.Client.ExtraArgs,
		WrapperDir:  executor.WrapperDirFor(cfg.Client.Command, wrapperDir, generatedAgents),
		RootDir:     agentRoot,
		IdleTimeout: cfg.Executor.IdleTimeout,
		Debug:       debugEnabled,
	}

	work, err := memorypipeline.NewUniqueAttemptDir(filepath.Join(runDir, "memory-rebuild"), newAttemptID)
	if err != nil {
		return err
	}
	agentCfg.RunDir = work // debug.log остаётся внутри рабочей директории попытки
	pipe := newRebuildPipeline(
		memorypipeline.Prompts{Reflect: prompts.Reflect, Aggregate: prompts.Aggregate, Prioritize: prompts.Prioritize, Update: prompts.Update},
		agentCfg)

	fmt.Printf("using current flow definition and current prompts for historical run %s\n", filepath.Base(runDir))
	manifestPath := filepath.Join(work, "manifest.json")
	meta := buildOperationMeta(cfg, f, flowPath, runDir, agentRoot, memDir, prompts, commit)

	// CLI создаёт манифест (статус "running", полные метаданные) и
	// центральным defer'ом переводит любой ещё нетерминальный манифест в
	// failed/cancelled при любом раннем возврате.
	if err := memorypipeline.WriteManifest(manifestPath, memorypipeline.NewRunningManifest(meta)); err != nil {
		return err
	}
	var finalErr error
	defer memorypipeline.FinalizeManifestOnExit(manifestPath, &finalErr, ctx)

	// Фаза 1: capture (без лока памяти).
	datasets, err := pipe.CaptureAll(ctx, memorypipeline.CaptureRequest{
		RunDir: runDir, WorkDir: work, Stages: f.Stages, ForceReflect: o.ForceReflect, ManifestPath: manifestPath})
	if err != nil {
		finalErr = err
		return fmt.Errorf("capture failed (see %s): %w", work, err)
	}

	// Фаза 2: лок памяти -> повторный commit-preflight -> finalize -> коммит
	// (всё под локом).
	memLock, err := memorypipeline.AcquireMemoryLock(ctx, memDir)
	if err != nil {
		finalErr = err
		return err
	}
	defer memLock.Close() //nolint:errcheck
	if commit {
		if err := commitPreflight(memDir); err != nil { // повторная проверка ПОД локом
			finalErr = err
			return err
		}
	}

	report, err := pipe.Finalize(ctx, memorypipeline.FinalizeRequest{
		Meta: meta, RunDir: runDir, WorkDir: work, MemoryDir: memDir, Stages: f.Stages, Memory: f.Memory,
		Datasets: datasets, DryRun: o.DryRun, ManifestPath: manifestPath})
	if err != nil {
		finalErr = err
		return fmt.Errorf("rebuild failed (see %s): %w", work, err)
	}
	printReport(report, o.DryRun)

	if o.DryRun {
		return nil // манифест уже "dry_run" из Finalize; defer увидит терминальный статус, no-op
	}
	if commit {
		if err := maybeCommit(memDir, report, manifestPath); err != nil {
			finalErr = err
			return err
		}
	}
	return memorypipeline.MarkManifestCompleted(manifestPath) // CLI пишет ФИНАЛЬНЫЙ "completed"
}

// hasWritableTarget сообщает, есть ли у флоу хотя бы одна стадия с
// reflect.mode w|rw (не script) — единственный способ, которым что-либо
// реально публикуется. Голый memory.mode:w без единой такой стадии НЕ
// делает флоу "writable": end-of-run проход пишет project-wide memory.md
// только из датасетов write-reflect стадий (review #12) — без них писать
// нечего.
func hasWritableTarget(f *flow.Flow) bool {
	for _, s := range f.Stages {
		if s.Reflect != nil && s.Reflect.CanWrite() && !s.IsScript() {
			return true
		}
	}
	return false
}

// stageIDsOf возвращает id всех стадий флоу в порядке объявления.
func stageIDsOf(f *flow.Flow) []string {
	ids := make([]string, len(f.Stages))
	for i, s := range f.Stages {
		ids[i] = s.ID
	}
	return ids
}

// checkStageSetMatchesExact сообщает ошибку с отсортированными списками
// missing/extra, если множество стадий в rs (реально запускавшихся в этом
// ране) не совпадает ТОЧНО с stageIDs (текущий флоу) — независимая от
// AllDone проверка (review #1): явный --run не проходит фильтр
// FindLatestCompletedRunDir, который делает то же самое неявно.
func checkStageSetMatchesExact(rs state.RunState, stageIDs []string) error {
	want := make(map[string]bool, len(stageIDs))
	for _, id := range stageIDs {
		want[id] = true
	}
	var missing, extra []string
	for id := range want {
		if _, ok := rs.Stages[id]; !ok {
			missing = append(missing, id)
		}
	}
	for id := range rs.Stages {
		if !want[id] {
			extra = append(extra, id)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	slices.Sort(missing)
	slices.Sort(extra)
	return fmt.Errorf("run stage set does not match current flow: missing %v, extra %v", missing, extra)
}

// newAttemptID строит id попытки memory-rebuild: <YYYYMMDD-HHMMSS>-<2 hex>.
// Таймстемп и случайность берутся здесь, в CLI, а не в пакете конвейера.
func newAttemptID() string {
	ts := time.Now().UTC().Format("20060102-150405")
	b := make([]byte, 1)
	_, _ = rand.Read(b)
	return ts + "-" + hex.EncodeToString(b)
}

// sha256Hex — hex-encoded SHA-256 data, общий хелпер для FlowSHA256/
// PromptSHA256 в buildOperationMeta.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// buildOperationMeta собирает OperationMeta для манифеста этой попытки:
// хэширование содержимого флоу и промптов (провенанс — что конкретно
// сгенерировало этот результат) — забота CLI, не пакета конвейера.
func buildOperationMeta(cfg config.Config, f *flow.Flow, flowPath, runDir, agentRoot, memDir string, prompts orchestrator.Prompts, effectiveCommit bool) memorypipeline.OperationMeta {
	flowSHA := ""
	if data, err := os.ReadFile(flowPath); err == nil {
		flowSHA = sha256Hex(data)
	}
	return memorypipeline.OperationMeta{
		RunID:           filepath.Base(runDir),
		RunPath:         runDir,
		FlowPath:        flowPath,
		FlowSHA256:      flowSHA,
		RootDir:         agentRoot,
		MemoryDir:       memDir,
		MemoryMode:      f.Memory.Mode,
		MaxRules:        f.Memory.MaxRules,
		EffectiveCommit: effectiveCommit,
		PromptsDir:      cfg.PromptsDir,
		PromptSHA256: map[string]string{
			memorypipeline.KindReflect:    sha256Hex([]byte(prompts.Reflect)),
			memorypipeline.KindAggregate:  sha256Hex([]byte(prompts.Aggregate)),
			memorypipeline.KindPrioritize: sha256Hex([]byte(prompts.Prioritize)),
			memorypipeline.KindUpdate:     sha256Hex([]byte(prompts.Update)),
		},
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// printReport печатает сводку прогона: счётчики плюс unified diff для
// изменённых (или потенциально изменённых, в dry-run) целей. НИКОГДА не
// печатает stdout агента/промпты/диалог — Report их и не содержит, только
// пути/хэши/diff.
func printReport(report memorypipeline.Report, dryRun bool) {
	changed := 0
	for _, t := range report.Targets {
		if t.Changed {
			changed++
		}
	}
	fmt.Printf("memory rebuild: %d dataset(s), %d target(s), %d changed\n",
		len(report.Datasets), len(report.Targets), changed)
	for _, t := range report.Targets {
		if !t.Changed {
			continue
		}
		verb := "changed"
		if dryRun {
			verb = "would change"
		}
		fmt.Printf("  %s %s (%s)\n", verb, t.FinalPath, t.Label)
		if t.Diff != "" {
			fmt.Print(t.Diff)
		}
	}
	if changed == 0 {
		fmt.Println("memory rebuild: no changes")
	}
}

// effectiveCommit разрешает итоговое решение "коммитить ли": явный флаг
// --commit/--no-commit важнее flow.yaml; --dry-run всегда выключает коммит
// (нечего коммитить, ничего не опубликовано).
func effectiveCommit(o rebuildOptions, f *flow.Flow) bool {
	if o.DryRun {
		return false
	}
	if o.CommitSet {
		return o.Commit
	}
	return f.Memory.Commit
}

// commitPreflight проверяет, что коммит в memDir возможен ДО дорогостоящей
// работы конвейера: memDir может ещё не существовать на диске (memory.path
// на первом ране), поэтому git-worktree ищется от ближайшего СУЩЕСТВУЮЩЕГО
// предка. Если предок не внутри git-репозитория — коммит просто не
// проверяется здесь (реальная git-ошибка всплывёт позже, при самом
// коммите); если внутри — staged-изменения под memDir должны отсутствовать,
// иначе rebuild рискует закоммитить что-то постороннее, уже поставленное в
// индекс пользователем.
func commitPreflight(memDir string) error {
	ancestor := nearestExistingAncestor(memDir)
	if err := exec.Command("git", "-C", ancestor, "rev-parse", "--show-toplevel").Run(); err != nil {
		return nil // не git-репозиторий — коммит позже даст свою явную ошибку
	}
	diffCmd := exec.Command("git", "-C", ancestor, "diff", "--cached", "--quiet", "--", memDir)
	if err := diffCmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return fmt.Errorf("memory dir %s already has staged changes; commit or unstage them first, or pass --no-commit", memDir)
		}
		return fmt.Errorf("git diff --cached preflight failed: %w", err)
	}
	return nil
}

// nearestExistingAncestor walks up from p until it finds a path that exists
// on disk (p itself, if it exists) — memDir may not have been created yet
// (a fresh memory.path).
func nearestExistingAncestor(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	cur := abs
	for {
		if _, err := os.Stat(cur); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return cur
		}
		cur = parent
	}
}

// maybeCommit коммитит ТОЛЬКО опубликованные изменённые цели — явным
// списком путей через memory.CommitPaths, а не весь memDir целиком (который
// может содержать посторонние staged файлы, уже поставленные в индекс
// пользователем вне этого rebuild). Ничего опубликовано не было -> no-op,
// манифест не трогается.
//
// На успехе патчит манифест CommitCreated/CommitSHA. На ОШИБКЕ (review #5) —
// переводит манифест в терминальный failed с добавленной в Errors причиной и
// проставленным FinishedAt ПРЯМО ЗДЕСЬ: к этому моменту манифест уже в
// StatusAwaitingCommit/StatusPublished (Finalize сам довёл его до этого
// терминального-для-Finalize статуса), которого нет в nonTerminalStatuses —
// деferred FinalizeManifestOnExit в rebuildHandler такой манифест НЕ
// трогает, так что без явного патча здесь упавший коммит навсегда читался
// бы как "awaiting_commit", а не "failed".
func maybeCommit(memDir string, report memorypipeline.Report, manifestPath string) error {
	var paths []string
	for _, t := range report.Targets {
		if t.Changed && t.Published {
			paths = append(paths, t.FinalPath)
		}
	}
	if len(paths) == 0 {
		return nil
	}

	committed, sha, commitErr := memory.CommitPaths(memDir, fmt.Sprintf("chore(memory): rebuild from %s", report.RunID), paths)

	m, lerr := memorypipeline.LoadManifest(manifestPath)
	if lerr != nil {
		// Манифест нечитаем — сама попытка коммита уже произошла (или
		// провалилась) независимо от этого; сообщаем об исходной ошибке
		// коммита, если она есть, иначе о невозможности записать исход.
		if commitErr != nil {
			return fmt.Errorf("memory files updated but commit failed: %w", commitErr)
		}
		return fmt.Errorf("commit succeeded but manifest is unreadable: %w", lerr)
	}

	if commitErr != nil {
		m.Status = memorypipeline.StatusFailed
		m.Errors = append(m.Errors, commitErr.Error())
		m.CommitError = commitErr.Error()
		m.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		_ = memorypipeline.WriteManifest(manifestPath, m)
		return fmt.Errorf("memory files updated but commit failed: %w", commitErr)
	}

	m.CommitCreated = committed
	m.CommitSHA = sha
	_ = memorypipeline.WriteManifest(manifestPath, m)
	return nil
}

// parseCommitFlags resolves --commit/--no-commit symmetrically: EACH flag
// only actually forces its side when it was both passed (Changed()) AND its
// value is true — mirroring how --commit already behaved (it reads
// GetBool("commit"), so "--commit=false" does not force a commit).
// Previously --no-commit forced Commit=false on bare Changed() alone,
// making "--no-commit=false" (explicitly passed but false) wrongly force
// no-commit instead of being a no-op. commitSet is true iff either flag
// actually forces its side; --commit and --no-commit both forcing is a
// mutual-exclusion error (review #12's original contract, preserved).
func parseCommitFlags(cmd *cobra.Command) (commitSet, commit bool, err error) {
	var commitForced, noCommitForced bool
	if cmd.Flags().Changed("commit") {
		v, _ := cmd.Flags().GetBool("commit")
		commitForced = v
	}
	if cmd.Flags().Changed("no-commit") {
		v, _ := cmd.Flags().GetBool("no-commit")
		noCommitForced = v
	}
	if commitForced && noCommitForced {
		return false, false, errors.New("--commit and --no-commit are mutually exclusive")
	}
	if commitForced {
		return true, true, nil
	}
	if noCommitForced {
		return true, false, nil
	}
	return false, false, nil
}

func newMemoryRebuildCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rebuild [flow.yaml]",
		Short: "Rebuild agent memory from a completed run's session logs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commitSet, commitVal, err := parseCommitFlags(cmd)
			if err != nil {
				return err
			}
			o := rebuildOptions{CommitSet: commitSet, Commit: commitVal}
			o.DryRun, _ = cmd.Flags().GetBool("dry-run")
			o.ForceReflect, _ = cmd.Flags().GetBool("force-reflect")
			o.RunID, _ = cmd.Flags().GetString("run")
			if len(args) == 1 {
				o.FlowArg = args[0]
			}
			return rebuildHandler(cmd.Context(), o)
		},
	}
	cmd.Flags().String("run", "", "explicit run id (default: latest completed run of the flow)")
	cmd.Flags().Bool("dry-run", false, "show diffs without writing memory or committing")
	cmd.Flags().Bool("force-reflect", false, "regenerate reflect datasets from logs")
	cmd.Flags().Bool("commit", false, "git-commit changed memory files")
	cmd.Flags().Bool("no-commit", false, "do not git-commit even if the flow enables it")
	return cmd
}

// resolveExplicitRunDir проверяет id, явно переданный через --run, и
// возвращает путь к его run-директории под runsBase. id обязан быть голым
// именем директории (без разделителей пути, без ".."/"." — Lstat вместо Stat,
// чтобы отклонить симлинк на run-директорию (review #7): подмена через
// симлинк не должна давать доступ за пределы runsBase.
func resolveExplicitRunDir(runsBase, flowName, id string) (string, error) {
	if id == "" || filepath.Base(id) != id || id == "." || id == ".." ||
		strings.ContainsAny(id, `/\`) {
		return "", fmt.Errorf("invalid run id %q: must be a bare run directory name", id)
	}
	prefix := flowName + "-"
	if len(id) <= len(prefix) || id[:len(prefix)] != prefix || id[len(prefix)] < '0' || id[len(prefix)] > '9' {
		return "", fmt.Errorf("run id %q does not belong to flow %q", id, flowName)
	}
	dir := filepath.Join(runsBase, id)
	info, err := os.Lstat(dir) // Lstat: отклонить симлинкованную run-директорию (review #7)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("run %q not found (or not a real directory) under %s", id, runsBase)
	}
	return dir, nil
}
