package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/assets"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
)

// Compile-time checks that *orchestrator.Orchestrator satisfies both
// server interfaces directly — a future signature drift here fails the
// build instead of surfacing as a runtime nil-interface panic in server.New.
var (
	_ server.StageActions     = (*orchestrator.Orchestrator)(nil)
	_ server.SecondaryActions = (*orchestrator.Orchestrator)(nil)
)

func newRunCmd() *cobra.Command {
	var maxParallel int
	var idleTimeout time.Duration
	var port int
	var requireApproval bool

	cmd := &cobra.Command{
		Use:   "run [flow.yaml]",
		Short: "Run a flow (or resume the latest run)",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, _ := os.UserHomeDir()
			cfg, err := config.LoadFrom(filepath.Join(home, config.AfmDir), fmDir())
			if err != nil {
				return err
			}
			if maxParallel > 0 {
				cfg.Executor.MaxParallel = maxParallel
			}
			if idleTimeout > 0 {
				cfg.Executor.IdleTimeout = idleTimeout
			}
			if cmd.Flags().Changed("port") {
				cfg.Server.Port = &port
			}

			flowPath, err := resolveFlowPath(args)
			if err != nil {
				return err
			}

			f, err := flow.ParseFile(flowPath)
			if err != nil {
				return fmt.Errorf("parse flow: %w", err)
			}

			// Docker self-re-exec: если включён Docker-режим и мы не внутри контейнера —
			// перезапускаем себя в Docker.
			if cfg.Docker.IsDockerEnabled() {
				absDir, absErr := filepath.Abs(rootDir)
				if absErr != nil {
					return fmt.Errorf("resolve project dir: %w", absErr)
				}
				if err := docker.CheckClaudeDockerAuth(cfg.Client.Command); err != nil {
					return err
				}
				if cfg.Docker.IsAutoShim() {
					if err := cfg.Docker.ValidateAgents(); err != nil {
						return err
					}
				}
				var generatedForMount map[string]bool
				var recipes map[string]config.AgentRecipe
				if cfg.Docker.IsAutoShim() {
					// Берём только recipe-агентов, которых реально использует флоу
					// (команда этапа или глобальный client.command). ReExec резолвит
					// секрет для КАЖДОЙ записи recipes и fail-fast'ит на первой
					// отсутствующей — без фильтрации определённый-но-неиспользуемый
					// агент без секрета заблокировал бы весь запуск, а секреты
					// неиспользуемых агентов попадали бы в контейнер (least-privilege).
					recipes = docker.UsedRecipes(f, cfg.Client.Command, cfg.Docker.Agents)
					// generatedForMount — то же самое множество ключей, чтобы
					// ScanCommands (mount) и ReExec (secret) работали с одним набором.
					generatedForMount = make(map[string]bool, len(recipes))
					for cmd := range recipes {
						generatedForMount[cmd] = true
					}
				}
				cmds := docker.ScanCommands(f, cfg.Client.Command, generatedForMount)
				mountCodexState := docker.UsesCodex(f, cfg.Client.Command, recipes)
				// File browser: манифест корней строим только если он включён —
				// BuildFileRootManifest всё равно возвращал бы непустой манифест
				// (корень проекта), и ReExec переключил бы публикацию порта на
				// loopback, даже когда пользователь file browser выключил.
				browserEnabled := cfg.Docker.FileBrowser.IsEnabled()
				var fileRoots docker.FileRootManifest
				if browserEnabled {
					fileRoots, err = docker.BuildFileRootManifest(absDir, cfg.Docker.ExtraMounts)
					if err != nil {
						return fmt.Errorf("build file root manifest: %w", err)
					}
				}
				port := cfg.Server.GetPort()
				// afm внутри Linux-контейнера не может открыть браузер на macOS-хосте
				// (runtime.GOOS=linux → xdg-open без display). Поэтому opener запускаем
				// на хосте ДО re-exec: это отдельный процесс, он переживает syscall.Exec
				// родителя и откроет dashboard, как только контейнер поднимет порт.
				if port > 0 && cfg.Server.IsOpenBrowser() {
					launchHostBrowserOpener(port)
				}
				// --dir должен быть абсолютным: относительный путь внутри контейнера
				// резолвился бы относительно -w (absDir) и дублировал вложенность.
				// Последний --dir выигрывает у возможного пользовательского флага
				// (cobra/pflag берёт последнее вхождение non-slice флага).
				return docker.ReExec(docker.ReExecConfig{
					Image:              cfg.Docker.GetImage(),
					ProjectDir:         absDir,
					Commands:           cmds,
					DashboardPort:      port,
					ExtraMounts:        cfg.Docker.ExtraMounts,
					ExtraArgs:          append(os.Args[1:], "--dir="+absDir),
					ClientCommand:      cfg.Client.Command,
					Recipes:            recipes,
					SecretsFile:        cfg.Docker.SecretsFile,
					MountCodexState:    mountCodexState,
					FileBrowserEnabled: browserEnabled,
					FileRoots:          fileRoots,
				})
			}

			// Apply flow-level overrides (CLI flag takes priority, then YAML, then config)
			if maxParallel == 0 && f.MaxParallel > 0 {
				cfg.Executor.MaxParallel = f.MaxParallel
			}

			prompts, err := loadPrompts(cfg.PromptsDir)
			if err != nil {
				return err
			}

			runDir, store, err := resolveRun(f)
			if err != nil {
				return err
			}
			defer store.Close()

			// Populate flow/stage display names from the flow definition. Works for
			// both new runs and resumed ones — names always come from the current
			// flow file, so they stay correct even if the flow was edited between
			// runs.
			store.SetFlowName(f.Name)
			{
				stageNames := make(map[string]string, len(f.Stages))
				for _, s := range f.Stages {
					if s.Name != "" {
						stageNames[s.ID] = s.Name
					}
				}
				store.SetStageNames(stageNames)
			}

			fmt.Printf("afm: running %q\n", f.Name)
			fmt.Printf("  run dir: %s\n", runDir)

			// Единый wrapper-dir: generated-врапперы (autoShim, только внутри
			// контейнера). На хосте врапперы не генерируются — реальные бинарники
			// используются напрямую.
			var wrapperSpecs []docker.WrapperSpec
			generatedAgents := map[string]bool{}
			if os.Getenv("AFM_IN_DOCKER") == "1" && cfg.Docker.IsAutoShim() {
				if err := cfg.Docker.ValidateAgents(); err != nil {
					return err
				}
				used := docker.UsedRecipeCommands(f, cfg.Client.Command, cfg.Docker.Agents)
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

			// Корень проекта для агентов (их CWD). Относительный root_dir
			// резолвится относительно afm-корня (--dir); пустой — агенты
			// наследуют CWD процесса afm (прежнее поведение).
			agentRootDir := f.RootDir
			if agentRootDir != "" && !filepath.IsAbs(agentRootDir) {
				agentRootDir = filepath.Join(rootDir, agentRootDir)
			}

			var memDir string
			if f.MemoryEnabled() {
				base := agentRootDir
				if base == "" {
					base = rootDir
				}
				memDir = f.Memory.Path
				if !filepath.IsAbs(memDir) {
					memDir = filepath.Join(base, memDir)
				}
			}

			orch := orchestrator.New(orchestrator.Options{
				RunDir:          runDir,
				Stages:          f.Stages,
				Store:           store,
				Config:          cfg,
				Prompts:         prompts,
				WrapperDir:      wrapperDir,
				GeneratedAgents: generatedAgents,
				GlobalPrompt:    f.Prompt,
				RootDir:         agentRootDir,
				RequireApproval: requireApproval,
				Debug:           debugEnabled,
				Memory:          f.Memory,
				MemoryDir:       memDir,
			})

			// Disable interactive flags when dashboard is not running
			if cfg.Server.GetPort() == 0 {
				for i := range f.Stages {
					if f.Stages[i].Interactive {
						f.Stages[i].Interactive = false
						fmt.Fprintf(os.Stderr, "warning: stage %q: interactive requires dashboard (server port > 0); running as non-interactive\n", f.Stages[i].ID)
					}
				}
			}

			// dashboardStarted — поднят ли HTTP-сервер дашборда. По флагу после
			// завершения флоу выдерживаем паузу (waitForDashboardDrain), чтобы UI
			// успел подтянуть терминальный статус до того, как процесс оборвёт
			// соединения. srv нужен и после if — waitForDashboardDrain опрашивает
			// его ConnectedClients().
			dashboardStarted := false
			var srv *server.Server

			// Start HTTP server if port > 0
			if cfg.Server.GetPort() > 0 {
				stageInteractive := make(map[string]bool, len(f.Stages))
				stageAutoApprove := make(map[string]bool, len(f.Stages))
				stageIsScript := make(map[string]bool, len(f.Stages))
				stageDependsOn := make(map[string][]string, len(f.Stages))
				stageButtons := make(map[string][]string, len(f.Stages))
				for _, st := range f.Stages {
					stageInteractive[st.ID] = st.Interactive
					stageAutoApprove[st.ID] = st.AutoApprove
					stageIsScript[st.ID] = st.IsScript()
					stageDependsOn[st.ID] = st.DependsOn
					stageButtons[st.ID] = st.Buttons.Labels()
				}
				srv = server.New(server.Config{
					Port:             cfg.Server.GetPort(),
					RunDir:           runDir,
					Description:      f.Description,
					StageInteractive: stageInteractive,
					StageAutoApprove: stageAutoApprove,
					StageIsScript:    stageIsScript,
					StageDependsOn:   stageDependsOn,
					StageButtons:     stageButtons,
					Store:            store,
					Theme:            cfg.EffectiveTheme(),
					SkinDir:          cfg.SkinDir,
					UIBus:            orch.UIBus(),
					Actions:          orch,
					Secondary:        orch,
				})
				addr, err := srv.Start()
				if err != nil {
					return fmt.Errorf("start dashboard: %w", err)
				}
				defer func() { _ = srv.Shutdown(context.Background()) }()
				dashboardStarted = true

				// Resolve listener address to localhost for client-facing URLs.
				// ln.Addr() may return [::]:port which is not reachable as a client URL.
				_, port, _ := net.SplitHostPort(addr)
				dashURL := fmt.Sprintf("http://localhost:%s", port) //nolint:revive // local dashboard is http
				orch.SetDashboardURL(dashURL)
				fmt.Printf("  dashboard: %s\n", dashURL)
				if cfg.Server.IsOpenBrowser() {
					// Локально — openBrowser; в Docker — хост-side opener уже запущен
					// (launchHostBrowserOpener, run.go:78), в контейнере xdg-open нет.
					if os.Getenv("AFM_IN_DOCKER") != "1" {
						openBrowser(dashURL)
					}
				} else {
					fmt.Println("  → open this URL in your browser to follow the run")
				}
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			if err := orch.Run(ctx); err != nil {
				return fmt.Errorf("run: %w", err)
			}

			fmt.Printf("afm: flow %q completed\n", f.Name)

			// Удерживаем дашборд после завершения флоу — см. waitForDashboardDrain.
			if dashboardStarted {
				fmt.Printf("  dashboard: holding at least %s for UI to render final state\n", dashboardExitGraceMin)
				waitForDashboardDrain(ctx, srv.ConnectedClients)
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&maxParallel, "max-parallel", 0, "max parallel stages (0=unlimited)")
	cmd.Flags().DurationVar(&idleTimeout, "idle-timeout", 0, "agent idle timeout")
	cmd.Flags().IntVar(&port, "port", 0, "dashboard port (0=use config)")
	cmd.Flags().BoolVar(&requireApproval, "require-approval", false, "fail if a stage plan needs approval but no dashboard is running")
	return cmd
}

// browserCmd возвращает команду открытия браузера для текущей ОС
// ("open" на macOS, "xdg-open" на Linux) или "" для неподдерживаемой ОС.
func browserCmd() string {
	switch runtime.GOOS {
	case "darwin":
		return "open"
	case "linux":
		return "xdg-open"
	default:
		return ""
	}
}

func openBrowser(url string) {
	cmd := browserCmd()
	if cmd == "" {
		return
	}
	//nolint:gosec // opening a local URL in the browser is safe
	_ = exec.Command(cmd, url).Start()
}

// launchHostBrowserOpener запускает на хосте фоновый помощник, который ждёт,
// пока dashboard поднимется на port, и открывает URL в браузере хоста.
// Нужен только для Docker-режима: afm внутри Linux-контейнера сам открыть
// браузер на macOS-хосте не может. Помощник — отдельный процесс (Start без
// Wait), поэтому он переживает syscall.Exec родителя, заменяющего afm на docker.
func launchHostBrowserOpener(port int) {
	openCmd := browserCmd()
	if openCmd == "" {
		return
	}
	url := fmt.Sprintf("http://localhost:%d", port)
	// Опрашиваем порт до ~60с; открываем браузер при первом ответе и выходим.
	script := fmt.Sprintf(`for i in $(seq 1 60); do curl -sf -m 1 %s >/dev/null 2>&1 && %s %s && break; sleep 1; done`, url, openCmd, url)
	c := exec.Command("sh", "-c", script)
	c.Stdin = nil
	c.Stdout = nil
	c.Stderr = nil
	//nolint:gosec // скрипт собран из констант и int-порта, не из пользовательского ввода
	_ = c.Start()
}

const extYAML = ".yaml"
const extYML = ".yml"

// dashboardExitGraceMin — безусловная пауза перед завершением процесса после
// успеха флоу, если поднят дашборд. Фронтенд опрашивает /api/status каждые 3с
// (POLL_INTERVAL_MS в use-status.ts) и обновляется по WS; 5с хватает, чтобы UI
// гарантированно увидел терминальный статус (done/failed), пока вкладка
// браузера активна.
const dashboardExitGraceMin = 5 * time.Second

// dashboardExitGraceMax — верхняя граница суммарного ожидания, пока к
// дашборду подключён хотя бы один WS-клиент. Свёрнутая/неактивная вкладка
// браузера троттлится браузером сильнее для setInterval-поллинга /api/status,
// чем для уже открытого WS-соединения — dashboardExitGraceMin один в этом
// случае недостаточен, UI «залипает» на последнем статусе (см. use-status.ts).
// Ограничена сверху, чтобы процесс (и, в Docker-режиме, контейнер) не завис
// навсегда из-за незакрытой вкладки.
const dashboardExitGraceMax = 2 * time.Minute

// dashboardDrainPoll — как часто проверять число подключённых WS-клиентов
// в течение dashboardExitGraceMax.
const dashboardDrainPoll = 2 * time.Second

// waitForDashboardDrain держит дашборд открытым после успешного завершения
// флоу: сначала dashboardExitGraceMin безусловно, затем — пока
// connectedClients() > 0, но не дольше dashboardExitGraceMax суммарно.
// Ctrl-C (ctx.Done()) прерывает ожидание немедленно.
func waitForDashboardDrain(ctx context.Context, connectedClients func() int) {
	waitForDashboardDrainWithTiming(ctx, connectedClients, dashboardExitGraceMin, dashboardExitGraceMax, dashboardDrainPoll)
}

// waitForDashboardDrainWithTiming — тело waitForDashboardDrain с
// параметризованными длительностями (тесты подставляют миллисекунды вместо
// реальных minGrace/maxGrace/pollInterval).
func waitForDashboardDrainWithTiming(ctx context.Context, connectedClients func() int, minGrace, maxGrace, pollInterval time.Duration) {
	select {
	case <-time.After(minGrace):
	case <-ctx.Done():
		return
	}

	deadline := time.Now().Add(maxGrace)
	for connectedClients() > 0 && time.Now().Before(deadline) {
		select {
		case <-time.After(pollInterval):
		case <-ctx.Done():
			return
		}
	}
}

func resolveFlowPath(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	entries, err := os.ReadDir(flowsDir())
	if err != nil {
		return "", errors.New("no flow file provided and " + flowsDir() + "/ not found")
	}
	var yamls []string
	for _, e := range entries {
		if !e.IsDir() && (filepath.Ext(e.Name()) == extYAML || filepath.Ext(e.Name()) == extYML) {
			yamls = append(yamls, filepath.Join(flowsDir(), e.Name()))
		}
	}
	if len(yamls) == 0 {
		return "", errors.New("no flow YAML files found in " + flowsDir() + "/")
	}
	if len(yamls) == 1 {
		return yamls[0], nil
	}
	return "", fmt.Errorf("multiple flow files found; specify one: %v", yamls)
}

func resolveRun(f *flow.Flow) (runDir string, store *state.Store, err error) {
	base := runsDir()

	stageIDs := make([]string, len(f.Stages))
	for i, s := range f.Stages {
		stageIDs[i] = s.ID
	}

	existing, lookErr := state.FindLatestRunDir(base, f.Name)
	if lookErr == nil {
		store, err = state.Open(existing, stageIDs)
		if err == nil {
			snap := store.Snapshot()
			if !snap.AllDone() {
				fmt.Printf("afm: resuming run %s\n", filepath.Base(existing))
				return existing, store, nil
			}
		} else {
			fmt.Fprintf(os.Stderr, "warning: failed to open existing run %s: %v; starting new run\n", filepath.Base(existing), err)
		}
		if store != nil {
			store.Close()
		}
	}

	runDir = filepath.Join(base, newRunID(f.Name))
	if err = os.MkdirAll(runDir, 0755); err != nil {
		return
	}
	store, err = state.Open(runDir, stageIDs)
	return
}

// newRunID строит уникальный id run: timestamp секундной гранулярности плюс
// короткий случайный суффикс, чтобы два запуска в одну секунду не делили
// одну директорию и один events.jsonl.
func newRunID(flowName string) string {
	ts := time.Now().Format("20060102-150405")
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s-%s", flowName, ts, hex.EncodeToString(b))
}

func loadPrompts(overrideDir string) (orchestrator.Prompts, error) {
	names := []string{"planning.md", "implementation.md", "review.md", "summary.md", "autonomous.md",
		"reflect.md", "aggregate.md", "prioritize.md", "update.md"}
	texts := make([]string, len(names))
	var custom, embedded []string
	for i, name := range names {
		text, fromOverride, err := assets.ReadPrompt(name, overrideDir)
		if err != nil {
			return orchestrator.Prompts{}, fmt.Errorf("read prompt %s: %w", name, err)
		}
		texts[i] = text
		if fromOverride {
			custom = append(custom, name)
		} else {
			embedded = append(embedded, name)
		}
	}
	// Кастомная prompts_dir может быть неполной — сообщаем, что взято из неё,
	// а что подхвачено из вкомпиленных дефолтов (вместо падения на нехватке).
	if overrideDir != "" {
		log.Printf("prompts: from %s: %v; embedded defaults: %v", overrideDir, custom, embedded)
	}
	return orchestrator.Prompts{
		Planning:       texts[0],
		Implementation: texts[1],
		Review:         texts[2],
		Summary:        texts[3],
		Autonomous:     texts[4],
		Reflect:        texts[5],
		Aggregate:      texts[6],
		Prioritize:     texts[7],
		Update:         texts[8],
	}, nil
}

// buildWrapperSpec строит WrapperSpec из recipe: прямой upstream URL bake'ится
// во враппер (прокси удалён — host-match не нужен). Вынесен из контейнерного
// цикла в run.go, чтобы быть тестируемым без поднятия Docker. Все поля WrapperSpec,
// включая Type и Bare, заполняются здесь — контейнерный цикл больше не
// собирает литерал вручную.
func buildWrapperSpec(cmd string, recipe config.AgentRecipe, bare bool) docker.WrapperSpec {
	return docker.WrapperSpec{
		Type:         recipe.Type,
		Command:      cmd,
		AuthTo:       recipe.Auth.EnvVarName(),
		BaseURL:      recipe.URL,
		Model:        recipe.Model,
		HasSysPrompt: recipe.SystemPrompt != "",
		Bare:         bare,
		MaxTurns:     recipe.MaxTurns,
	}
}
