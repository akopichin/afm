package docker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
)

// containerHome — домашний каталог non-root пользователя внутри контейнера.
// Маунты ~/.claude и ~/.afm накладываются поверх containerHome/.claude и .afm,
// а entrypoint (gosu, см. docker-entrypoint.sh) выставляет HOME=containerHome и
// дропает привилегии до хостового uid — поэтому записи в тома принадлежат
// пользователю хоста, а не root.
const containerHome = "/home/afm"

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
	ExtraMounts     []string                      // доп. хост-директории (могут начинаться с ~) → монтируются :ro
	ExtraArgs       []string                      // os.Args[1:]
	ClientCommand   string                        // имя агента из config (для проверки auth при command: claude)
	Recipes         map[string]config.AgentRecipe // autoShim: команды с recipe → генерируются, секрет → transient env
	SecretsFile     string                        // опц. override для default-слоёв secrets.env
	MountCodexState bool                          // codex использует ~/.codex (OAuth) → монтировать read-only в /tmp/host-codex (entrypoint копирует в $HOME/.codex, см. UsesCodex)
}

// CheckClaudeDockerAuth проверяет, что при использовании command: claude в Docker
// задан один из поддерживаемых auth env vars. macOS Keychain недоступен из
// Linux-контейнера, поэтому OAuth-сессия из ~/.claude.json там не работает.
// Возвращает ошибку с инструкцией, если auth не настроена.
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

// defaultExecFunc запускает docker как дочерний процесс и ждёт его завершения.
// Ранее использовался syscall.Exec (замена текущего образа), но на macOS 26
// execve() из подписанного Go-бинаря в docker вызывает SIGKILL — macOS блокирует
// смену образа между разными code identities. exec.Command обходит это
// ограничение: docker запускается дочерним процессом, а не заменяет текущий.
// Возвращает *SubprocessExitError — caller обязан завершить процесс с этим кодом.
//
// SIGINT/SIGTERM хостовому процессу (Ctrl-C, kill) без явной обработки просто
// убивали САМ ЭТОТ процесс дефолтной Go/OS-диспозицией — docker run (дочерний
// процесс, exec.Command его не форвардит сигналы автоматически) оставался
// сиротой и продолжал крутить контейнер бесконечно, никак не зная о смерти
// родителя. Ловим сигнал сами и форвардим его в docker run: тот в foreground-
// режиме (без -d, как здесь), получив SIGINT/SIGTERM, сам штатно останавливает
// контейнер — остаётся просто дождаться его настоящего завершения.
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

// SetExecFunc заменяет функцию exec (только для тестов).
// ВНИМАНИЕ: execFunc — это изменяемое состояние уровня пакета; эти хелперы
// предназначены только для последовательного (sequential) использования в
// тестах и НЕ безопасны при t.Parallel().
func SetExecFunc(f func(string, []string, []string) error) {
	execFunc = f
}

// ResetExecFunc возвращает функцию exec к дефолтной (только для тестов).
func ResetExecFunc() {
	execFunc = defaultExecFunc
}

// ScanCommands возвращает список нестандартных (не claude, не generated) агентов
// из flow, которые нужно смонтировать в Docker-контейнер. generated — команды с
// recipe (autoShim): они генерируются в контейнере, бинарник не монтируется.
// Бинарники, не найденные в PATH, молча пропускаются.
func ScanCommands(f *flow.Flow, globalCmd string, generated map[string]bool) []CommandMount {
	seen := make(map[string]bool)
	var mounts []CommandMount

	addCmd := func(cmd string) {
		if cmd == "" || cmd == config.ClaudeCommand || generated[cmd] || seen[cmd] {
			return
		}
		seen[cmd] = true
		hostPath, err := exec.LookPath(cmd)
		if err != nil {
			return
		}
		mounts = append(mounts, CommandMount{
			HostPath:      hostPath,
			ContainerName: filepath.Base(cmd),
		})
	}

	addCmd(globalCmd)
	for _, s := range f.Stages {
		addCmd(s.Command)
	}
	return mounts
}

// UsedRecipeCommands returns the recipe keys that are actually referenced as a
// stage command or the global client command. Only these get generated wrappers.
func UsedRecipeCommands(f *flow.Flow, globalCmd string, recipes map[string]config.AgentRecipe) map[string]bool {
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
		}
	}
	return used
}

// UsedRecipes projects the used-command set onto the recipes map, returning only
// the recipes whose command is actually referenced by the flow (a stage command
// or the global client command). This is the same rule UsedRecipeCommands applies
// — no duplication of the skip-claude / used-in-stages logic.
//
// Why: ReExec resolves a secret for EVERY entry in ReExecConfig.Recipes and
// fail-fasts on the first missing one. If run.go passed the entire cfg.Docker.
// Agents map, a defined-but-unused agent with a missing secret would block the
// whole run (and leak its secret into the container env). Filtering first keeps
// secret resolution and secret env injection scoped to agents the flow actually
// uses — least-privilege.
func UsedRecipes(f *flow.Flow, globalCmd string, all map[string]config.AgentRecipe) map[string]config.AgentRecipe {
	used := UsedRecipeCommands(f, globalCmd, all)
	out := make(map[string]config.AgentRecipe, len(used))
	for cmd := range used {
		out[cmd] = all[cmd]
	}
	return out
}

// codexAdapterCommand — имя команды-адаптера codex CLI → claude stream-json
// (см. scripts/codex-as-claude.sh), baked в образ рядом с openai-as-claude/
// cursor-as-claude. Используется и напрямую (command: codex-as-claude, без
// recipe), и как exec-цель generated-враппера recipe-типа "codex".
const codexAdapterCommand = "codex-as-claude"

// UsesCodex reports whether the flow (a stage command or the global client
// command) invokes codex — either directly via codexAdapterCommand, or
// indirectly via a used recipe of type "codex" — used to gate mounting the
// host's ~/.codex OAuth state (see ReExec). usedRecipes should already be
// filtered to commands the flow actually references (UsedRecipes), consistent
// with the least-privilege rule already applied to recipe secrets.
func UsesCodex(f *flow.Flow, globalCmd string, usedRecipes map[string]config.AgentRecipe) bool {
	if globalCmd == codexAdapterCommand {
		return true
	}
	if f != nil {
		for _, s := range f.Stages {
			if s.Command == codexAdapterCommand {
				return true
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

// ReExec заменяет текущий процесс на docker run с нужными монтированиями.
// Возвращает ошибку только если docker не найден в PATH; в случае успеха
// syscall.Exec никогда не возвращает управление.
func ReExec(cfg ReExecConfig) error {
	dockerBin, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("docker not found in PATH: %w", err)
	}

	home, _ := os.UserHomeDir()
	if home == "" {
		return errors.New("cannot determine home directory for Docker mounts")
	}

	args := []string{"docker", "run", "--rm"}

	if isTTY() {
		args = append(args, "-it")
	}

	// Dashboard: пробрасываем порт на хост, иначе UI недоступен извне контейнера.
	if cfg.DashboardPort > 0 {
		args = append(args, "-p", fmt.Sprintf("%d:%d", cfg.DashboardPort, cfg.DashboardPort))
	}

	// Монтируем проект по тому же абсолютному пути — os.Args проходят без изменений.
	// ~/.claude и ~/.afm — в домашний каталог контейнера (containerHome), чтобы
	// claude/агенты находили их через $HOME (его выставляет entrypoint-shim).
	args = append(args,
		"-v", cfg.ProjectDir+":"+cfg.ProjectDir,
		"-v", home+"/.claude:"+containerHome+"/.claude",
		"-v", home+"/.afm:"+containerHome+"/.afm",
		"-w", cfg.ProjectDir,
	)
	// ВНИМАНИЕ: ~/.claude.json намеренно НЕ монтируем. claude при попытке обновить
	// :ro-файл (атомарный write-through-rename) ломается и квартитит его как
	// "corrupted: JSON Parse error", после чего агент падает. Вместо этого claude
	// создаёт свежий container-local конфиг в containerHome/.claude.json — этого
	// достаточно: auth claude идёт через CLAUDE_CODE_OAUTH_TOKEN (long-lived token,
	// генерируется командой `claude setup-token`) или ANTHROPIC_API_KEY из env,
	// а не из .claude.json. Предупреждение "config not found" — нефатальный шум.

	// Нестандартные агенты монтируем read-only.
	for _, m := range cfg.Commands {
		args = append(args, "-v", m.HostPath+":/usr/local/bin/"+m.ContainerName+":ro")
	}

	// Доп. хост-директории кастомных агентов (токены/конфиги вне ~/.claude).
	// Пути с ~ монтируем в домашний каталог контейнера (containerHome/…), чтобы
	// скрипты находили их через $HOME; абсолютные пути — по тому же пути (как проект).
	for _, m := range cfg.ExtraMounts {
		hostPath := expandHome(m, home)
		containerPath := expandHome(m, containerHome)
		args = append(args, "-v", hostPath+":"+containerPath+":ro")
	}

	// Codex OAuth-состояние (~/.codex): монтируем read-only во временный путь
	// контейнера — entrypoint (root, до gosu) копирует его в $HOME/.codex
	// (writable), чтобы codex мог обновлять auth.json (refresh token), не задевая
	// хостовый ~/.codex. Гейтим MountCodexState, чтобы не тащить OAuth-состояние
	// во флоу, которые codex не используют (см. UsesCodex).
	if cfg.MountCodexState {
		codexHostDir := filepath.Join(home, ".codex")
		if info, statErr := os.Stat(codexHostDir); statErr == nil && info.IsDir() {
			args = append(args, "-v", codexHostDir+":/tmp/host-codex:ro")
		}
	}

	// Окружение внутри контейнера.
	// AFM_HOST_UID/GID: entrypoint дропает привилегии (gosu) до хостового
	// пользователя, чтобы записи в примонтированные тома принадлежали пользователю
	// хоста, а не root. Под non-root claude разрешает --dangerously-skip-permissions
	// без дополнительных флагов, поэтому IS_SANDBOX больше не нужен.
	args = append(args,
		"-e", "AFM_IN_DOCKER=1",
		"-e", "AFM_HOST_UID="+strconv.Itoa(os.Getuid()),
		"-e", "AFM_HOST_GID="+strconv.Itoa(os.Getgid()),
	)
	// AFM_DEBUG — не секрет, передаём значением, чтобы --debug/AFM_DEBUG на
	// хосте включал логирование промптов и внутри контейнера.
	if os.Getenv("AFM_DEBUG") != "" {
		args = append(args, "-e", "AFM_DEBUG="+os.Getenv("AFM_DEBUG"))
	}
	// Передаём секреты только в bare-форме `-e KEY` (без значения): docker-клиент
	// наследует окружение afm (см. os.Environ() в вызове execFunc ниже) и сам
	// подставит значение. Так секрет никогда не попадает в argv `docker run` и
	// не светится в `ps aux`, history и audit-логах хоста.
	dockerForwardEnvVars := append([]string{"ANTHROPIC_BASE_URL"}, config.ClaudeAuthEnvVars...)
	for _, key := range dockerForwardEnvVars {
		if os.Getenv(key) != "" {
			args = append(args, "-e", key)
		}
	}

	// autoShim: резолвим секреты recipe на хосте и передаём в контейнер как
	// transient bare-form env (значение в env afm, не в argv docker). Generated
	// врапперы внутри контейнера читают $AFM_SECRET_<CMD>/$AFM_SYSPROMPT_<CMD> и
	// unset'ят их до exec claude. Команды с recipe НЕ монтируются (ScanCommands).
	if len(cfg.Recipes) > 0 {
		secrets, err := LoadSecretLayers(cfg.SecretsFile, cfg.ProjectDir)
		if err != nil {
			return err
		}
		for cmd, recipe := range cfg.Recipes {
			name := envName(cmd)
			// codex — единственный тип, для которого Validate() допускает пустой
			// Auth (авторизация идёт через смонтированную ~/.codex, не через
			// секрет). Пропускаем резолв ТОЛЬКО для codex, иначе ResolveAuthValue("", ...)
			// фейлит на пустом auth.from и валит весь запуск даже когда секрет не нужен.
			// Для остальных типов (openai/cursor) пустой auth.from при заданном
			// auth.to — это misconfiguration, которая должна fail-fast здесь, а не
			// молча всплыть позже в контейнере как "OPENAI_API_KEY is not set".
			if recipe.Type == config.RecipeTypeCodex && recipe.Auth.From == "" {
				continue
			}
			val, vErr := ResolveAuthValue(recipe.Auth.From, secrets)
			if vErr != nil {
				return fmt.Errorf("agent %s: %w", cmd, vErr)
			}
			_ = os.Setenv("AFM_SECRET_"+name, val) // процесс далее exec'нет docker; утечки в argv нет
			args = append(args, "-e", "AFM_SECRET_"+name)
			if recipe.SystemPrompt != "" {
				// system_prompt опционален: при ошибке резолва (файла нет и т.п.) — тихий
				// fallback, claude запускается со своим стандартным system prompt
				// (без --append-system-prompt-file) вместо glm-специфичного.
				if sp, spErr := ResolveSystemPrompt(recipe.SystemPrompt); spErr == nil && sp != "" {
					_ = os.Setenv("AFM_SYSPROMPT_"+name, sp)
					args = append(args, "-e", "AFM_SYSPROMPT_"+name)
				}
			}
		}
	}

	// Образ + оригинальные аргументы afm.
	args = append(args, cfg.Image)
	args = append(args, cfg.ExtraArgs...)

	return execFunc(dockerBin, args, os.Environ())
}

// isTTY сообщает, подключён ли stdin к настоящему терминалу.
// Используется, чтобы добавлять `-it` к `docker run` только в интерактивном
// режиме. Раньше здесь был эвристический анализ os.ModeCharDevice, но он
// ошибочно срабатывал на /dev/null (тоже символьное устройство) — и в
// неинтерактивных сценариях (скрипты, CI, фоновый запуск) добавлялся `-it`,
// после чего docker падал с "the input device is not a TTY". term.IsTerminal
// делает честную проверку через ioctl(TCGETS/TIOCGETA).
func isTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// expandHome раскрывает ведущую ~ в абсолютный путь относительно home.
// "~" → home, "~/foo" → home+"/foo"; прочее возвращается как есть.
func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return home + p[1:] // p[1:] == "/…"
	}
	return p
}
