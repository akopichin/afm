# golang.org/x/term

Честная кроссплатформенная проверка "stdin подключён к реальному терминалу". Аудитория: клеточка
`pkg/docker`.

## TTY-детекция для условного добавления `-it` к `docker run`

`os.ModeCharDevice`-эвристика ложно срабатывает на `/dev/null` (тоже char device), из-за чего в
неинтерактивных сценариях (CI, фоновый запуск) `docker run` ошибочно получал `-it` и падал с
"the input device is not a TTY". `term.IsTerminal` делает честную проверку через `ioctl`
(`TCGETS`/`TIOCGETA`), а не по типу файла:

```go
import "golang.org/x/term"

func isTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}
```

Результат используется, чтобы решить, добавлять ли флаг `-it` к аргументам `docker run` — добавлять
только когда `isTTY()` истинно.

## Особенности

- Используется единственная функция `term.IsTerminal(fd int) bool`; сложные сценарии (raw mode,
  чтение размера терминала и т.п.) в проекте не встречаются.
- Проверка обязана выполняться именно над файловым дескриптором `os.Stdin.Fd()`, а не над
  `os.Stdin.Stat().Mode()` — последнее и есть источник бага с `/dev/null`.
