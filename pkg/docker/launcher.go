package docker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/akopichin/afm/pkg/flow"
)

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
	args = append(args,
		"-v", cfg.ProjectDir+":"+cfg.ProjectDir,
		"-v", home+"/.claude:/root/.claude",
		"-v", home+"/.afm:/root/.afm",
		"-w", cfg.ProjectDir,
	)
	// ВНИМАНИЕ: ~/.claude.json намеренно НЕ монтируем. claude при попытке обновить
	// :ro-файл (атомарный write-through-rename) ломается и квартитит его как
	// "corrupted: JSON Parse error", после чего агент падает. Вместо этого claude
	// создаёт свежий container-local конфиг в /root/.claude.json — этого достаточно:
	// auth кастомных агентов (glm*) идёт через ANTHROPIC_AUTH_TOKEN из env, а не из
	// .claude.json. Предупреждение "config not found" при этом — нефатальный шум.

	// Нестандартные агенты монтируем read-only.
	for _, m := range cfg.Commands {
		args = append(args, "-v", m.HostPath+":/usr/local/bin/"+m.ContainerName+":ro")
	}

	// Доп. хост-директории кастомных агентов (токены/конфиги вне ~/.claude).
	// Пути с ~ монтируем в домашний каталог контейнера (/root/…), чтобы скрипты
	// находили их через $HOME; абсолютные пути — по тому же пути (как проект).
	for _, m := range cfg.ExtraMounts {
		hostPath := expandHome(m, home)
		containerPath := expandHome(m, "/root")
		args = append(args, "-v", hostPath+":"+containerPath+":ro")
	}

	// Окружение внутри контейнера.
	// IS_SANDBOX=1: контейнер — это песочница, поэтому claude разрешает
	// --dangerously-skip-permissions под root (иначе он отказывается работать).
	args = append(args, "-e", "AFM_IN_DOCKER=1", "-e", "IS_SANDBOX=1")
	// Передаём секреты только в bare-форме `-e KEY` (без значения): docker-клиент
	// наследует окружение afm (см. os.Environ() в вызове execFunc ниже) и сам
	// подставит значение. Так секрет никогда не попадает в argv `docker run` и
	// не светится в `ps aux`, history и audit-логах хоста.
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"} {
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
