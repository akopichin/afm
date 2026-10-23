package main

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/akopichin/afm/pkg/browser"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
)

// interCycleDrainGrace — сколько dashboard держит финальное состояние рана между циклами repeat. Вкладку
// не ждём: следующий цикл поднимет dashboard на том же порту, а открытая вкладка найдёт новый run ID сама.
var interCycleDrainGrace = dashboardExitGraceMinimum

// executeCycle — seam для тестов: один проход флоу.
var executeCycle = executeFlow

// cycleResult — итог одного прохода флоу после записи EndRun.
type cycleResult struct {
	RunID string
	// Resumed — ран продолжил журнал, найденный при старте процесса, а не создан этим процессом.
	Resumed bool
	// Completed — журнал рана содержит run_finished и все стадии done. orch.Run()==nil этого не гарантирует:
	// ран с failed-стадией тоже возвращает nil.
	Completed bool
}

// cycleLoop — всё, что неизменно между циклами одного процесса `afm run`.
type cycleLoop struct {
	flowPath        string        // определяется один раз до первого цикла
	cfg             config.Config // CLI > config; max_parallel флоу накладывается заново на каждом цикле
	cliMaxParallel  bool          // --max-parallel задан и сильнее max_parallel флоу
	worktreeName    string
	worktreeDir     string
	requireApproval bool
	forceNewRun     bool // clear-флаги: первый цикл не продолжает чужой ран
}

// run проходит флоу и, пока включён repeat, повторяет его после каждого успешного рана. Единственный
// signal-контекст охватывает все циклы и межцикловое ожидание dashboard. created — ID существующих ранов,
// созданных этим процессом (продолженный при старте ран и уже удалённые в него не входят): allowlist для
// очистки старых ранов. Очистка (keep_runs выполнившегося flow) идёт после создания run dir следующего цикла и
// один раз при завершении повторения после успешного цикла; при ошибке или отмене раны остаются как есть.
func (l cycleLoop) run(ctx context.Context, f *flow.Flow, hooks []lifecyclehooks.RegisteredHook) (created []string, err error) {
	opts := runOptions{
		requireApproval: l.requireApproval,
		worktreeDir:     l.worktreeDir,
		forceNewRun:     l.forceNewRun,
		runName:         runNameFor(l.worktreeName, f.Name),
	}
	if config.ReExecedIntoContainer() {
		var err error
		if opts.containerBrowser, err = takeContainerTransport(hooks); err != nil {
			return nil, err
		}
	}

	for completed := 0; ; {
		opts.cycle = completed + 1
		// executeFlow зовёт afterRun после успешного рана, до паузы dashboard: по этой ранней оценке выбирается
		// пауза (короткая между циклами или полная перед выходом). Решение о следующем цикле — ниже.
		var next cycleDecision
		opts.afterRun = func() bool {
			next = l.decideNext(ctx, f, hooks, opts.cycle)
			return next.continues()
		}
		res, err := executeCycle(ctx, f, cycleConfig(l.cfg, f, l.cliMaxParallel), hooks, opts)
		if res.RunID != "" && !res.Resumed {
			created = append(created, res.RunID)
		}
		if err != nil {
			return created, err
		}
		if !f.Repeat.Enabled {
			return created, nil
		}
		if !res.Completed {
			return created, fmt.Errorf("cycle %d: run %s did not complete successfully; repeat stopped", opts.cycle, res.RunID)
		}
		completed++
		if next.continues() {
			// Ранняя оценка выбрала лишь длину паузы dashboard; за паузу flow.yaml мог измениться, поэтому
			// создание следующего рана решает свежее чтение перед run dir.
			next = l.decideNext(ctx, f, hooks, completed)
		}
		switch {
		// next может быть ранней оценкой из opts.afterRun, посчитанной до паузы dashboard: Ctrl+C во время
		// самой паузы должен приводить к прерыванию, даже если next уже нёс err/stopReason до отмены.
		case ctx.Err() != nil:
			return created, runExitError(ctx, ctx.Err())
		case next.err != nil:
			return created, next.err
		case next.stopReason != "":
			fmt.Printf("afm: repeat finished: %s\n", next.stopReason)
			created = prune(created, f.Repeat.KeepRuns)
			return created, nil
		}

		keepRuns := f.Repeat.KeepRuns // правило завершившегося цикла; created не включает новый ран
		opts.onRunCreated = func(string) { created = prune(created, keepRuns) }
		f, hooks = next.flow, next.hooks
		opts.forceNewRun = true // продолжать можно только ран первого входа процесса
		opts.previousRunID = res.RunID
		fmt.Printf("afm: starting cycle %d\n", completed+1)
	}
}

// cycleDecision — что делать после успешного цикла. Нулевое значение (отмена по Ctrl+C) — не продолжать и не
// сообщать причину: run вернёт ошибку прерывания.
type cycleDecision struct {
	flow  *flow.Flow
	hooks []lifecyclehooks.RegisteredHook
	// stopReason — повторение закончено без ошибки (достигнут max_cycles или repeat выключен).
	stopReason string
	// err — flow.yaml невалиден или правка требует перезапуска afm run.
	err error
}

func (d cycleDecision) continues() bool { return d.flow != nil }

// decideNext решает судьбу цикла после done успешных. Достигнутый max_cycles текущего flow flow.yaml не
// читает; иначе файл перечитывается до создания run dir следующего цикла.
func (l cycleLoop) decideNext(ctx context.Context, f *flow.Flow, hooks []lifecyclehooks.RegisteredHook, done int) cycleDecision {
	if !repeatContinues(f.Repeat, done) {
		return cycleDecision{stopReason: fmt.Sprintf("max_cycles %d reached", f.Repeat.MaxCycles)}
	}
	if ctx.Err() != nil {
		return cycleDecision{}
	}
	next, nextHooks, stopReason, err := l.reload(f, hooks, done)
	return cycleDecision{flow: next, hooks: nextHooks, stopReason: stopReason, err: err}
}

// repeatContinues сообщает, нужен ли ещё один цикл после completed успешных.
func repeatContinues(r flow.RepeatConfig, completed int) bool {
	return r.Enabled && (r.MaxCycles == 0 || completed < r.MaxCycles)
}

// cycleConfig накладывает max_parallel флоу на копию базовой конфигурации: CLI > flow > config. База не
// меняется, поэтому удалённый из flow.yaml max_parallel возвращает значение конфигурации.
func cycleConfig(base config.Config, f *flow.Flow, cliMaxParallel bool) config.Config {
	if !cliMaxParallel && f.MaxParallel > 0 {
		base.Executor.MaxParallel = f.MaxParallel
	}
	return base
}

// reload перечитывает flow.yaml перед циклом completed+1. Непустой stopReason — повторение закончено без
// ошибки (repeat выключен или достигнут новый max_cycles); ошибка — flow.yaml невалиден или правка требует
// перезапуска afm run. Всё это происходит до создания run dir.
func (l cycleLoop) reload(prev *flow.Flow, prevHooks []lifecyclehooks.RegisteredHook, completed int) (next *flow.Flow, hooks []lifecyclehooks.RegisteredHook, stopReason string, err error) {
	fail := func(cause error) error {
		return fmt.Errorf("cycle %d not started (%d completed): %w", completed+1, completed, cause)
	}
	next, err = flow.ParseFile(l.flowPath)
	if err != nil {
		return nil, nil, "", fail(fmt.Errorf("parse flow: %w", err))
	}
	if err := config.ValidateVerifySpecs(next, l.cfg); err != nil {
		return nil, nil, "", fail(fmt.Errorf("verify: %w", err))
	}
	switch {
	case !next.Repeat.Enabled:
		return nil, nil, fmt.Sprintf("repeat is disabled in %s", l.flowPath), nil
	case !repeatContinues(next.Repeat, completed):
		return nil, nil, fmt.Sprintf("max_cycles %d reached", next.Repeat.MaxCycles), nil
	}
	if err := l.checkReloadCompatible(prev, next); err != nil {
		return nil, nil, "", fail(err)
	}
	hooks = combineRunHooks(l.cfg, next)
	if config.ReExecedIntoContainer() {
		if err := carryHookEnv(prevHooks, hooks); err != nil {
			return nil, nil, "", fail(err)
		}
	}
	return next, hooks, "", nil
}

// restartError — правка flow.yaml, которую нельзя применить внутри живого процесса.
func restartError(format string, args ...any) error {
	return fmt.Errorf("%s; restart `afm run` to apply it", fmt.Sprintf(format, args...))
}

// checkReloadCompatible отклоняет правки, меняющие worktree или Docker-монтирование: их создаёт
// `afm run` один раз до первого цикла.
func (l cycleLoop) checkReloadCompatible(prev, next *flow.Flow) error {
	if prev.Name != next.Name {
		return restartError("flow name changed from %q to %q", prev.Name, next.Name)
	}
	prevWorktrees := worktreesEnabled(l.worktreeName, l.cfg, prev)
	if nextWorktrees := worktreesEnabled(l.worktreeName, l.cfg, next); prevWorktrees != nextWorktrees {
		return restartError("worktrees changed from %t to %t", prevWorktrees, nextWorktrees)
	}
	if prevWorktrees {
		prevBase, err := worktreeBase(absRootDir(), prev)
		if err != nil {
			return err
		}
		nextBase, err := worktreeBase(absRootDir(), next)
		if err != nil {
			return err
		}
		if prevBase != nextBase {
			return restartError("root_dir changed the worktree base from %q to %q", prevBase, nextBase)
		}
	}
	if config.ReExecedIntoContainer() {
		return l.checkContainerReload(prev, next)
	}
	return nil
}

// checkContainerReload: контейнер монтирует проект и команды агентов один раз, поэтому root_dir вне проекта и
// другая Docker-проекция (команды, recipes, Codex-state) требуют нового контейнера.
func (l cycleLoop) checkContainerReload(prev, next *flow.Flow) error {
	project := absRootDir()
	prevRoot, err := absoluteAgentRoot(project, prev)
	if err != nil {
		return err
	}
	nextRoot, err := absoluteAgentRoot(project, next)
	if err != nil {
		return err
	}
	if nextRoot != prevRoot && !isSubPath(project, nextRoot) {
		return restartError("root_dir %q is outside the Docker-mounted project %q and needs a new mount", nextRoot, project)
	}
	prevProj, err := projectDocker(prev, l.cfg)
	if err != nil {
		return err
	}
	nextProj, err := projectDocker(next, l.cfg)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(prevProj, nextProj) {
		return restartError("agent commands, docker recipes or Codex state changed, but the container mounts are fixed")
	}
	return nil
}

// dockerProjection — то, что из flow и config попадает в mount/transport контейнера. Её строит reexecFlow на
// хосте, prepareRunEnvironment в контейнере, и ею же перечитывание flow.yaml сравнивает новый flow со старым.
type dockerProjection struct {
	Recipes         map[string]config.AgentRecipe // только используемые рецепты
	Generated       map[string]bool               // команды рецептов, для которых создаются wrapper'ы
	Extra           []string                      // команды профилей помощников; отсортированы
	Commands        []string                      // docker.CommandNames: команды, которые ищутся на хосте
	MountCodexState bool
}

func projectDocker(f *flow.Flow, cfg config.Config) (dockerProjection, error) {
	p := dockerProjection{Extra: assistantProfileCommands(cfg, f)}
	slices.Sort(p.Extra) // команды берутся из map профилей: порядок должен быть одинаковым при каждом вызове
	p.Commands = docker.CommandNames(f, cfg.Client.Command, p.Extra)
	if cfg.Docker.IsAutoShim() {
		if err := cfg.Docker.ValidateAgents(); err != nil {
			return p, err
		}
		// Mount discovery and secret resolution use the same set of used recipes.
		p.Recipes = docker.UsedRecipes(f, cfg.Client.Command, cfg.Docker.Agents, p.Extra...)
		p.Generated = make(map[string]bool, len(p.Recipes))
		for cmd := range p.Recipes {
			p.Generated[cmd] = true
		}
	}
	p.MountCodexState = docker.UsesCodex(f, cfg.Client.Command, p.Recipes)
	return p, nil
}

// takeContainerTransport один раз читает одноразовый transport хоста — env-секреты хуков и дескриптор
// browser — и удаляет его из окружения процесса. Дальше циклы пользуются сохранёнными значениями.
func takeContainerTransport(hooks []lifecyclehooks.RegisteredHook) (*browser.Descriptor, error) {
	if err := resolveLifecycleEnv(absRootDir(), hooks); err != nil {
		return nil, err
	}
	desc := descriptorFromEnv(true)
	// Strip consumed AFM_BROWSER_* transport from afm's env so even direct child processes cannot inherit
	// the token.
	browser.UnsetTransportVars()
	return desc, nil
}

// carryHookEnv переносит разрешённый env хуков из прошлого flow в перечитанный. В контейнере transport уже
// удалён, поэтому состав хуков с env (ID, слой, спецификация, порядок) должен совпасть; остальные поля хука
// берутся из нового flow.yaml.
func carryHookEnv(prev, next []lifecyclehooks.RegisteredHook) error {
	prevIdx, nextIdx := envHookIndexes(prev), envHookIndexes(next)
	if len(prevIdx) != len(nextIdx) {
		return restartError("the set of lifecycle hooks with env changed (%d -> %d)", len(prevIdx), len(nextIdx))
	}
	for i, ni := range nextIdx {
		p, n := prev[prevIdx[i]], next[ni]
		if p.Hook.ID != n.Hook.ID || p.StageID != n.StageID || !maps.Equal(p.Hook.Env, n.Hook.Env) {
			return restartError("lifecycle hook %q with env changed", n.Hook.ID)
		}
	}
	for i, ni := range nextIdx {
		next[ni].Hook.ResolvedEnv = prev[prevIdx[i]].Hook.ResolvedEnv
	}
	return nil
}

func envHookIndexes(hooks []lifecyclehooks.RegisteredHook) []int {
	var idx []int
	for i, h := range hooks {
		if len(h.Hook.Env) > 0 {
			idx = append(idx, i)
		}
	}
	return idx
}
