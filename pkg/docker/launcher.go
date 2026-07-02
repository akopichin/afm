package docker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"

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
	Image         string
	ProjectDir    string // абсолютный путь к директории проекта
	Commands      []CommandMount
	DashboardPort int      // порт dashboard; при >0 пробрасывается на хост через -p
	ExtraMounts   []string // доп. хост-директории (могут начинаться с ~) → монтируются :ro
	ExtraArgs     []string // os.Args[1:]
	ClientCommand string   // имя агента из config (для проверки auth при command: claude)
}

const claudeCommand = "claude"

// claudeAuthEnvVars — env vars, через которые claude CLI принимает токены в Docker.
// macOS хранит OAuth-токены в Keychain, который недоступен из Linux-контейнера;
// поэтому auth должна идти через один из этих env vars.
var claudeAuthEnvVars = []string{
	"CLAUDE_CODE_OAUTH_TOKEN", // long-lived token: `claude setup-token`
	"ANTHROPIC_API_KEY",       // API-ключ Anthropic
	"ANTHROPIC_AUTH_TOKEN",    // auth token для кастомных шлюзов
}

// CheckClaudeDockerAuth проверяет, что при использовании command: claude в Docker
// задан один из поддерживаемых auth env vars. macOS Keychain недоступен из
// Linux-контейнера, поэтому OAuth-сессия из ~/.claude.json там не работает.
// Возвращает ошибку с инструкцией, если auth не настроена.
func CheckClaudeDockerAuth(clientCommand string) error {
	if clientCommand != claudeCommand && clientCommand != "" {
		return nil
	}
	for _, key := range claudeAuthEnvVars {
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

// defaultExecFunc — дефолтная обёртка над syscall.Exec; вынесена в именованную
// переменную уровня пакета, чтобы ResetExecFunc восстанавливала ссылку, а не
// пересоздавала литерал (иначе gosec G702 срабатывает на теле функции).
var defaultExecFunc = func(argv0 string, argv []string, envv []string) error {
	return syscall.Exec(argv0, argv, envv)
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

// ScanCommands возвращает список нестандартных (не claude) агентов из flow,
// которые нужно смонтировать в Docker-контейнер.
// Бинарники, не найденные в PATH, молча пропускаются.
func ScanCommands(f *flow.Flow, globalCmd string) []CommandMount {
	seen := make(map[string]bool)
	var mounts []CommandMount

	addCmd := func(cmd string) {
		if cmd == "" || cmd == "claude" || seen[cmd] {
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
	// Передаём секреты только в bare-форме `-e KEY` (без значения): docker-клиент
	// наследует окружение afm (см. os.Environ() в вызове execFunc ниже) и сам
	// подставит значение. Так секрет никогда не попадает в argv `docker run` и
	// не светится в `ps aux`, history и audit-логах хоста.
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if os.Getenv(key) != "" {
			args = append(args, "-e", key)
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
