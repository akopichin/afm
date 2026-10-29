package docker

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/secrets"
	"github.com/akopichin/afm/pkg/timezone"
)

// containerHome — домашний каталог non-root пользователя внутри контейнера.
const containerHome = "/home/afm"

// WorktreeEnvVar — имя переменной окружения, через которую хост передаёт путь к уже подготовленному git
// worktree в контейнер. Следует паттерну FileRootsEnvVar (хост считает значение, форвардит через env,
// контейнер потребляет под AFM_IN_DOCKER=1), а не AFM_SIDEAGENT (переразрешение тоггла в контейнере) —
// создать worktree может только хост.
const WorktreeEnvVar = "AFM_DOCKER_WORKTREE"

// SkillLibrariesEnvVar — библиотеки скилов (через os.PathListSeparator), которые хост проверил перед стартом
// контейнера. Только хост видит, ведёт ли путь к той же библиотеке, поэтому без рестарта годятся лишь они.
const SkillLibrariesEnvVar = "AFM_DOCKER_SKILL_LIBRARIES"

// CommandMount описывает нестандартный агент для монтирования в контейнер.
type CommandMount struct {
	HostPath      string
	ContainerName string
}

// ReExecConfig параметры для перезапуска afm в Docker.
type ReExecConfig struct {
	Image           string
	ProjectDir      string // абсолютный путь к директории проекта
	Commands        []CommandMount
	DashboardPort   int                           // порт dashboard; при >0 пробрасывается на хост через -p
	ExtraMounts     config.ExtraMounts            // доп. хост-директории (могут начинаться с ~) → монтируются :ro
	ExtraArgs       []string                      // os.Args[1:]
	ClientCommand   string                        // имя агента из config (для проверки auth при command: claude)
	Recipes         map[string]config.AgentRecipe // autoShim: команды с recipe → генерируются, секрет → transient env
	SecretsFile     string                        // опц. override для default-слоёв secrets.env
	MountCodexState bool                          // codex использует ~/.codex (OAuth) → монтировать read-only в /tmp/host-codex (entrypoint копирует в $HOME/.codex, см. UsesCodex)

	// FileBrowserEnabled — включён ли встроенный файловый браузер проекта.
	FileBrowserEnabled bool
	// FileRoots — манифест корней file browser (см. BuildFileRootManifest), закодированный и передаваемый в
	// контейнер только когда FileBrowserEnabled и Roots непусты.
	FileRoots FileRootManifest

	// WorktreeDir — абсолютный путь к git worktree, уже подготовленному ХОСТОМ (resolveWorktree в cmd/afm)
	// до вызова ReExec. Пусто — worktree выключен, агенты в контейнере работают в основном чекауте. Путь
	// лежит под ProjectDir (containment-гейт — обязанность вызывающего кода), поэтому отдельный bind-mount
	// не нужен: ProjectDir уже смонтирован по идентичному пути.
	WorktreeDir string

	// SkillLibraries — библиотеки скилов, проверенные хостом; см. SkillLibrariesEnvVar.
	SkillLibraries []string

	// Hooks — итоговый список lifecycle-хуков, собранный ВЫЗЫВАЮЩИМ КОДОМ ДО этой докер-ветки.
	Hooks []lifecyclehooks.RegisteredHook

	// Managed browser capability transport. Менеджер поднят на ХОСТЕ до docker run; контейнерный afm достаёт
	// endpoint/token/epoch/ app-url из env и строит per-stage descriptor.
	BrowserEnabled  bool
	BrowserToken    string // → bare -e AFM_BROWSER_TRANSPORT_TOKEN (секрет)
	BrowserEndpoint string // host.docker.internal:<managerPort> → -e AFM_BROWSER_ENDPOINT
	BrowserEpoch    string // host-generated epoch → -e AFM_BROWSER_EPOCH
	// Фиксированная публикация порта тестируемого приложения: host-порт выбран на
	// хосте ДО docker run (app-url известен заранее), -p 127.0.0.1:<host>:<container>.
	BrowserAppHostPort      int
	BrowserAppContainerPort int
	BrowserAppURL           string // http://127.0.0.1:<hostPort> → -e AFM_BROWSER_APP_URL (не секрет)
}

// UsesLifecycleSecrets проверяет наличие Env у хуков, чтобы не загружать и не передавать ненужные секреты.
func UsesLifecycleSecrets(hooks []lifecyclehooks.RegisteredHook) bool {
	for _, rh := range hooks {
		if len(rh.Hook.Env) > 0 {
			return true
		}
	}
	return false
}

// CheckClaudeDockerAuth проверяет, что при использовании command: claude в Docker задан один из
// поддерживаемых auth env vars. Возвращает ошибку с инструкцией, если auth не настроена.
func CheckClaudeDockerAuth(clientCommand string) error {
	if clientCommand != config.ClaudeCommand && clientCommand != "" {
		return nil
	}
	for _, key := range config.ClaudeAuthEnvVars {
		if os.Getenv(key) != "" {
			return nil
		}
	}
	return fmt.Errorf(
		"command: claude в Docker-режиме требует auth через env var.\n" +
			"macOS Keychain (OAuth-сессия) недоступен из Linux-контейнера.\n\n" +
			"Варианты:\n" +
			"  1. Claude Pro/Max: сгенерировать долгоживущий токен:\n" +
			"       claude setup-token\n" +
			"     и добавить в ~/.zshrc:\n" +
			"       export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-si-...\n\n" +
			"  2. API-ключ Anthropic:\n" +
			"       export ANTHROPIC_API_KEY=sk-ant-api-<ключ>",
	)
}

// SubprocessExitError передаётся вызывающей стороне вместо os.Exit:
// caller должен вызвать os.Exit(err.Code), чтобы отразить код завершения docker.
type SubprocessExitError struct {
	Code int
}

func (e *SubprocessExitError) Error() string {
	return fmt.Sprintf("exit: %d", e.Code)
}

// defaultExecFunc запускает docker как дочерний процесс и ждёт его завершения. Возвращает
// *SubprocessExitError — caller обязан завершить процесс с этим кодом.
var defaultExecFunc = func(argv0 string, argv []string, envv []string) error {
	c := exec.Command(argv0, argv[1:]...) //nolint:gosec
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = envv

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	if err := c.Start(); err != nil {
		return err
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- c.Wait() }()

	var err error
	select {
	case sig := <-sigCh:
		_ = c.Process.Signal(sig)
		err = <-waitErr
	case err = <-waitErr:
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &SubprocessExitError{Code: exitErr.ExitCode()}
		}
		return err
	}
	return &SubprocessExitError{Code: 0}
}

// execFunc — заменяемая в тестах обёртка над syscall.Exec.
var execFunc = defaultExecFunc

// SetExecFunc заменяет функцию exec.
func SetExecFunc(f func(string, []string, []string) error) {
	execFunc = f
}

// ResetExecFunc возвращает функцию exec к дефолтной (только для тестов).
func ResetExecFunc() {
	execFunc = defaultExecFunc
}

// ScanCommands возвращает список нестандартных агентов из flow, которые нужно смонтировать в
// Docker-контейнер. Бинарники, не найденные в PATH, молча пропускаются.
func ScanCommands(f *flow.Flow, globalCmd string, generated map[string]bool) []CommandMount {
	return ScanCommandsWithExtra(f, globalCmd, generated, nil)
}

// ScanCommandsWithExtra is ScanCommands plus extra command names to scan for mounting — T8.5: assistant
// (helper-agent) profile commands are not part of f.Stages, so without this they never got a Docker mount
// even when the flow had collaboration enabled with a non-standard profile command. extra commands go
// through the exact same PATH lookup / generated / claude-bare exclusion rules as flow-stage commands; a
// command present in both a stage and extra is only mounted once (shared `seen` dedup).
func ScanCommandsWithExtra(f *flow.Flow, globalCmd string, generated map[string]bool, extra []string) []CommandMount {
	addCmd, mounts := newCommandMountCollector(generated)
	for _, cmd := range CommandNames(f, globalCmd, extra) {
		addCmd(cmd)
	}
	return mounts()
}

// CommandNames — единый перечень команд агентов, которые Docker-подготовка ищет на хосте: глобальная команда,
// команды стадий и их verify-агентов, extra. Без пустых имён, встроенного claude и повторов; отсортирован,
// поэтому порядок монтирований и сравнение проекций не зависят от порядка map. Сравнивать надо имена, а не
// найденные mount'ы: в контейнере хостовых бинарников нет в PATH.
func CommandNames(f *flow.Flow, globalCmd string, extra []string) []string {
	names := map[string]bool{globalCmd: true}
	if f != nil {
		for _, s := range f.Stages {
			names[s.Command] = true
			for _, cmd := range stageVerifyCommands(s) {
				names[cmd] = true
			}
		}
	}
	for _, cmd := range extra {
		names[cmd] = true
	}
	delete(names, "")
	delete(names, config.ClaudeCommand)
	return slices.Sorted(maps.Keys(names))
}

// newCommandMountCollector returns an addCmd closure (dedups against generated/claude-bare/already-seen, and
// resolves the host binary via PATH, silently skipping anything not found) and a mounts accessor for the
// CommandMounts collected so far.
func newCommandMountCollector(generated map[string]bool) (addCmd func(cmd string), mounts func() []CommandMount) {
	seen := make(map[string]bool)
	var collected []CommandMount

	addCmd = func(cmd string) {
		if cmd == "" || cmd == config.ClaudeCommand || generated[cmd] || seen[cmd] {
			return
		}
		seen[cmd] = true
		hostPath, err := exec.LookPath(cmd)
		if err != nil {
			return
		}
		collected = append(collected, CommandMount{
			HostPath:      hostPath,
			ContainerName: filepath.Base(cmd),
		})
	}
	mounts = func() []CommandMount { return collected }
	return addCmd, mounts
}

// stageVerifyCommands включает verify-агентов в discovery для монтирования и генерации wrapper'ов.
func stageVerifyCommands(s flow.Stage) []string {
	return s.VerifyAgentCommands()
}

// UsedRecipeCommands returns the recipe keys that are actually referenced as a stage/verify command, the
// global client command or one of extra (helper-profile commands — the same list ScanCommandsWithExtra
// mounts). Only these get generated wrappers; a used recipe is never mounted from the host.
func UsedRecipeCommands(f *flow.Flow, globalCmd string, recipes map[string]config.AgentRecipe, extra ...string) map[string]bool {
	used := map[string]bool{}
	check := func(cmd string) {
		if cmd == "" || cmd == config.ClaudeCommand {
			return
		}
		if _, ok := recipes[cmd]; ok {
			used[cmd] = true
		}
	}
	check(globalCmd)
	if f != nil {
		for _, s := range f.Stages {
			check(s.Command)
			for _, cmd := range stageVerifyCommands(s) {
				check(cmd)
			}
		}
	}
	for _, cmd := range extra {
		check(cmd)
	}
	return used
}

// UsedRecipes projects the used-command set onto the recipes map, returning only the recipes whose command
// is actually referenced by the flow. Why: ReExec resolves a secret for EVERY entry in ReExecConfig.Recipes
// and fail-fasts on the first missing one — passing the entire cfg.Docker.Agents map would let a
// defined-but-unused agent with a missing secret block the whole run (and leak its secret into container env).
func UsedRecipes(f *flow.Flow, globalCmd string, all map[string]config.AgentRecipe, extra ...string) map[string]config.AgentRecipe {
	used := UsedRecipeCommands(f, globalCmd, all, extra...)
	out := make(map[string]config.AgentRecipe, len(used))
	for cmd := range used {
		out[cmd] = all[cmd]
	}
	return out
}

// codexAdapterCommand — имя команды-адаптера codex CLI → claude stream-json, baked в образ рядом с
// openai-as-claude/ cursor-as-claude.
const codexAdapterCommand = "codex-as-claude"

// UsesCodex gates mounting host OAuth state; usedRecipes must contain only recipes referenced by the flow.
func UsesCodex(f *flow.Flow, globalCmd string, usedRecipes map[string]config.AgentRecipe) bool {
	if globalCmd == codexAdapterCommand {
		return true
	}
	if f != nil {
		for _, s := range f.Stages {
			if s.Command == codexAdapterCommand {
				return true
			}
			for _, cmd := range stageVerifyCommands(s) {
				if cmd == codexAdapterCommand {
					return true
				}
			}
		}
	}
	for _, r := range usedRecipes {
		if r.Type == config.RecipeTypeCodex {
			return true
		}
	}
	return false
}

// ReExec заменяет текущий процесс на docker run с нужными монтированиями. Возвращает ошибку только если
// docker не найден в PATH; в случае успеха syscall.Exec никогда не возвращает управление.
func ReExec(cfg ReExecConfig) error {
	dockerBin, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("docker not found in PATH: %w", err)
	}

	home, _ := os.UserHomeDir()
	if home == "" {
		return errors.New("cannot determine home directory for Docker mounts")
	}

	// --init: Docker's init (tini) becomes PID 1 and reaps orphans. Agents leave background processes behind
	// that afm kills at the end of a turn; orphaned ones are adopted by PID 1, and afm (which would otherwise be
	// PID 1) never waits for them, so they would stay zombies. tini forwards signals to afm and exits with its
	// exit code, so Ctrl-C, docker stop and SubprocessExitError work as before.
	args := []string{"docker", "run", "--rm", "--init"}

	if isTTY() {
		args = append(args, "-it")
	}

	// Dashboard: пробрасываем порт на хост, иначе UI недоступен извне контейнера. Граница не зависит от
	// фичефлагов.
	if cfg.DashboardPort > 0 {
		bind := fmt.Sprintf("127.0.0.1:%d:%d", cfg.DashboardPort, cfg.DashboardPort)
		args = append(args, "-p", bind)
	}

	// Монтируем проект по тому же абсолютному пути — os.Args проходят без изменений.
	args = append(args,
		"-v", cfg.ProjectDir+":"+cfg.ProjectDir,
		"-v", home+"/.claude:"+containerHome+"/.claude",
		"-v", home+"/.afm:"+containerHome+"/.afm",
		"-w", cfg.ProjectDir,
	)
	// ВНИМАНИЕ: ~/.claude.json намеренно НЕ монтируем. Предупреждение "config not found" — нефатальный шум.

	// Нестандартные агенты монтируем read-only.
	for _, m := range cfg.Commands {
		args = append(args, "-v", m.HostPath+":/usr/local/bin/"+m.ContainerName+":ro")
	}

	// Доп. хост-директории кастомных агентов (токены/конфиги вне ~/.claude).
	for _, m := range cfg.ExtraMounts {
		hostPath := ResolveMountPath(cfg.ProjectDir, m.Path, home)
		containerPath := ResolveMountPath(cfg.ProjectDir, m.Path, containerHome)
		args = append(args, "-v", hostPath+":"+containerPath+":ro")
	}

	// Codex OAuth-состояние: монтируем read-only во временный путь контейнера — entrypoint копирует его в
	// $HOME/.codex, чтобы codex мог обновлять auth.json, не задевая хостовый ~/.codex.
	if cfg.MountCodexState {
		codexHostDir := filepath.Join(home, ".codex")
		if info, statErr := os.Stat(codexHostDir); statErr == nil && info.IsDir() {
			args = append(args, "-v", codexHostDir+":/tmp/host-codex:ro")
		}
	}

	// Окружение внутри контейнера. Под non-root claude разрешает --dangerously-skip-permissions без
	// дополнительных флагов, поэтому IS_SANDBOX больше не нужен.
	args = append(args,
		"-e", "AFM_IN_DOCKER=1",
		"-e", "AFM_HOST_UID="+strconv.Itoa(os.Getuid()),
		"-e", "AFM_HOST_GID="+strconv.Itoa(os.Getgid()),
	)
	// Часовой пояс хоста — не секрет, передаём значением: зона контейнера (обычно
	// UTC) не совпадает с хостовой, а dashboard показывает время по зоне хоста.
	hostTZ := timezone.Host()
	args = append(args,
		"-e", timezone.EnvHostTimeZone+"="+hostTZ.Name,
		"-e", timezone.EnvHostUTCOffsetMinutes+"="+strconv.Itoa(hostTZ.OffsetMinutes),
	)
	// Без IANA-имени передаём правила зоны: контейнер пересчитывает текущее
	// смещение при каждом status-запросе, включая переход DST после запуска.
	if hostTZ.Name == "" {
		if zoneFile := timezone.HostZoneFile(); zoneFile != "" {
			args = append(args,
				"-v", zoneFile+":"+timezone.ContainerZoneFile+":ro",
				"-e", timezone.EnvHostZoneFile+"="+timezone.ContainerZoneFile,
			)
		}
	}
	// Несекретные переключатели передаём значением (не bare -e) и ТОЛЬКО если хост задал их явно:
	// config резолвится заново внутри контейнера, поэтому env с хоста должен доехать и переопределить
	// смонтированный конфиг, а невыставленный env не должен "придумываться" — иначе хост и контейнер разойдутся.
	// AFM_DEBUG включает логирование промптов, AFM_ACCOUNTING — отображение стоимости, остальные — тогглы фич.
	for _, name := range []string{"AFM_DEBUG", "AFM_ACCOUNTING", "AFM_SIDEAGENT", "AFM_COLLABORATION", "AFM_ADD_STAGE"} {
		if value := os.Getenv(name); value != "" {
			args = append(args, "-e", name+"="+value)
		}
	}
	// File browser: манифест корней (не секрет) передаём значением — контейнер
	// читает его один раз при старте, чтобы знать, какие пути показывать в UI.
	if cfg.FileBrowserEnabled && len(cfg.FileRoots.Roots) > 0 {
		if enc, encErr := EncodeFileRootManifest(cfg.FileRoots); encErr == nil {
			args = append(args, "-e", FileRootsEnvVar+"="+enc)
		}
	}
	// Worktree: путь (не секрет — виден и так через bind-mount) передаём значением, чтобы resolveWorktree
	// внутри контейнера знало, куда указывать CWD агентов, не повторяя git-работу хоста.
	if cfg.WorktreeDir != "" {
		args = append(args, "-e", WorktreeEnvVar+"="+cfg.WorktreeDir)
	}
	if len(cfg.SkillLibraries) > 0 {
		args = append(args, "-e", SkillLibrariesEnvVar+"="+strings.Join(cfg.SkillLibraries, string(os.PathListSeparator)))
	}
	// Передаём секреты только в bare-форме `-e KEY`: docker-клиент наследует окружение afm и сам подставит
	// значение.
	dockerForwardEnvVars := append([]string{"ANTHROPIC_BASE_URL"}, config.ClaudeAuthEnvVars...)
	for _, key := range dockerForwardEnvVars {
		if os.Getenv(key) != "" {
			args = append(args, "-e", key)
		}
	}

	// autoShim: резолвим секреты recipe на хосте и передаём в контейнер как transient bare-form env. Команды
	// с recipe НЕ монтируются (ScanCommands).
	if len(cfg.Recipes) > 0 {
		loadedSecrets, err := LoadSecretLayers(cfg.SecretsFile, cfg.ProjectDir)
		if err != nil {
			return err
		}
		for cmd, recipe := range cfg.Recipes {
			name := envName(cmd)
			// codex — единственный тип, для которого Validate допускает пустой Auth.
			if recipe.Type == config.RecipeTypeCodex && recipe.Auth.From == "" {
				continue
			}
			val, vErr := secrets.ResolveRef(recipe.Auth.From, loadedSecrets)
			if vErr != nil {
				return fmt.Errorf("agent %s: %w", cmd, vErr)
			}
			_ = os.Setenv("AFM_SECRET_"+name, val) // процесс далее exec'нет docker; утечки в argv нет
			args = append(args, "-e", "AFM_SECRET_"+name)
			if recipe.SystemPrompt != "" {
				// system_prompt опционален: при ошибке резолва — тихий fallback, claude запускается со своим
				// стандартным system prompt вместо glm-специфичного.
				if sp, spErr := ResolveSystemPrompt(recipe.SystemPrompt); spErr == nil && sp != "" {
					_ = os.Setenv("AFM_SYSPROMPT_"+name, sp)
					args = append(args, "-e", "AFM_SYSPROMPT_"+name)
				}
			}
		}
	}

	// Lifecycle hooks: секреты хука резолвим на ХОСТЕ и передаём в контейнер тем же паттерном, что
	// autoShim-секреты выше — transient bare-form `-e`. Секрет-файлы в контейнер НЕ монтируются.
	if UsesLifecycleSecrets(cfg.Hooks) {
		loadedSecrets, err := LoadSecretLayers(cfg.SecretsFile, cfg.ProjectDir)
		if err != nil {
			return err
		}
		for hookIdx, rh := range cfg.Hooks {
			if len(rh.Hook.Env) == 0 {
				continue
			}
			for varIdx, varName := range lifecyclehooks.SortedEnvKeys(rh.Hook) {
				val, rErr := secrets.ResolveRef(string(rh.Hook.Env[varName]), loadedSecrets)
				if rErr != nil {
					return fmt.Errorf("lifecycle hook %q env %q: %w", rh.Hook.ID, varName, rErr)
				}
				name := lifecyclehooks.TransportName(hookIdx, varIdx)
				_ = os.Setenv(name, val) // процесс далее exec'нет docker; утечки в argv нет
				args = append(args, "-e", name)
			}
		}
	}

	// Managed browser capability: публикуем фиксированный app-порт на loopback и передаём транспорт. Менеджер
	// уже слушает на хосте.
	if cfg.BrowserEnabled {
		if cfg.BrowserAppHostPort > 0 && cfg.BrowserAppContainerPort > 0 {
			args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", cfg.BrowserAppHostPort, cfg.BrowserAppContainerPort))
		}
		_ = os.Setenv("AFM_BROWSER_TRANSPORT_TOKEN", cfg.BrowserToken) // процесс далее exec'нет docker; утечки в argv нет
		args = append(args, "-e", "AFM_BROWSER_TRANSPORT_TOKEN")
		if cfg.BrowserEndpoint != "" {
			args = append(args, "-e", "AFM_BROWSER_ENDPOINT="+cfg.BrowserEndpoint)
		}
		if cfg.BrowserEpoch != "" {
			args = append(args, "-e", "AFM_BROWSER_EPOCH="+cfg.BrowserEpoch)
		}
		if cfg.BrowserAppURL != "" {
			args = append(args, "-e", "AFM_BROWSER_APP_URL="+cfg.BrowserAppURL)
		}
	}

	// Образ + оригинальные аргументы afm.
	args = append(args, cfg.Image)
	args = append(args, cfg.ExtraArgs...)

	return execFunc(dockerBin, args, os.Environ())
}

// isTTY сообщает, подключён ли stdin к настоящему терминалу. Используется, чтобы добавлять `-it` к `docker
// run` только в интерактивном режиме.
func isTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// expandHome — тонкий алиас над secrets.ExpandHome, чтобы не трогать все
// call sites в этом файле.
func expandHome(p, home string) string {
	return secrets.ExpandHome(p, home)
}

// ExtraMountContainerPaths возвращает container-side путь для каждой записи mounts — ту же резолюцию, что
// ReExec использует для `-v host:container`.
func ExtraMountContainerPaths(projectContainerPath string, mounts config.ExtraMounts) []string {
	paths := make([]string, 0, len(mounts))
	for _, m := range mounts {
		paths = append(paths, ResolveMountPath(projectContainerPath, m.Path, containerHome))
	}
	return paths
}

// ResolveMountPath резолвит путь extra_mounts в абсолютный путь: "~..." — относительно home, уже
// абсолютный — как есть, прочий относительный — относительно projectRoot.
func ResolveMountPath(projectRoot, path, home string) string {
	if strings.HasPrefix(path, "~") {
		return expandHome(path, home)
	}
	if !filepath.IsAbs(path) {
		return filepath.Join(projectRoot, path)
	}
	return path
}
