# cobra (github.com/spf13/cobra)

CLI-фреймворк для построения `afm` — корневой команды и её подкоманд. Аудитория: клеточка `cmd/afm`.

## Дерево команд

Корневая команда создаётся в `newRootCmd()` и содержит общий персистентный флаг `--dir` и
`PersistentPreRunE`, вычисляющий базовую директорию `.afm` по приоритету флаг > `AFM_DIR` > `.`.
Подкоманды регистрируются через `root.AddCommand(...)`, каждая — отдельная функция-конструктор
`newXxxCmd() *cobra.Command`, возвращающая независимый `*cobra.Command`.

```go
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "afm",
		Short: "Orchestrate multi-stage AI flows",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			rootDir = resolveRootDir(rootDir, os.Getenv("AFM_DIR"))
			return nil
		},
	}
	root.PersistentFlags().StringVar(&rootDir, "dir", "", "base directory for .afm")
	root.AddCommand(newRunCmd(), newCheckCmd(), newApproveCmd(), newReviseCmd(),
		newRetryCmd(), newInitCmd(), newListCmd(), newInstallSkillsCmd())
	return root
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
```

## Подкоманда с локальными флагами

Локальные флаги объявляются через замыкание над переменными вне `RunE`, бизнес-логика — внутри
`RunE: func(cmd *cobra.Command, args []string) error`. Позиционные аргументы приходят в `args`.

```go
func newRunCmd() *cobra.Command {
	var maxParallel int
	var idleTimeout time.Duration
	var port int

	cmd := &cobra.Command{
		Use:   "run [flow.yaml]",
		Short: "Run a flow (or resume the latest run)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// ... бизнес-логика, использующая maxParallel/idleTimeout/port и args
			return nil
		},
	}
	cmd.Flags().IntVar(&maxParallel, "max-parallel", 0, "override max parallel agents")
	cmd.Flags().DurationVar(&idleTimeout, "idle-timeout", 0, "override idle timeout")
	cmd.Flags().IntVar(&port, "port", 0, "override dashboard port")
	return cmd
}
```

Проверка "флаг передан явно, а не просто равен zero-value" — `cmd.Flags().Changed("port")`.

## Ошибки

`RunE` возвращает `error`; `Execute()` в `main()` печатает её (cobra делает это сама) и код завершения
устанавливается через `os.Exit(1)` при ненулевой ошибке — отдельный `os.Exit` в каждой подкоманде не нужен.
