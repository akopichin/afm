# golang.org/x/sys/windows

Win32 file-locking API, недоступный в стандартной библиотеке `os`/`syscall` на Windows. Аудитория:
клеточка `pkg/progress` (Windows-ветка кроссплатформенной блокировки прогресс-файла).

## Эксклюзивная блокировка файла на Windows

На Unix-подобных системах для advisory-блокировки файла используется `syscall.Flock` из стандартной
библиотеки (`//go:build !windows`). На Windows аналога в стандартной библиотеке нет — используется
`windows.LockFileEx` из `golang.org/x/sys/windows`, изолированный отдельным build-тегом `//go:build windows`:

```go
//go:build windows

import "golang.org/x/sys/windows"

func (l *Lock) Lock() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		f.Close()
		return fmt.Errorf("LockFileEx: %w", err)
	}
	l.f = f
	return nil
}

func (l *Lock) TryLock() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	ol := new(windows.Overlapped)
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, ol); err != nil {
		f.Close()
		return fmt.Errorf("lock busy: %w", err)
	}
	l.f = f
	return nil
}
```

`Lock` — блокирующий вызов (флаг `LOCKFILE_EXCLUSIVE_LOCK` без `LOCKFILE_FAIL_IMMEDIATELY`).
`TryLock` — неблокирующий (добавляет `LOCKFILE_FAIL_IMMEDIATELY`, возвращает ошибку "занято" сразу,
не дожидаясь освобождения).

## Особенности

- Оба build-тега (`windows` и `!windows`) реализуют один и тот же контракт (`Lock()`/`TryLock() error`
  на типе `*Lock`) — платформенная реализация подставляется компилятором по `GOOS`, вызывающий код
  платформо-независим.
- `Overlapped`-структура обязательна для `LockFileEx` даже при синхронном использовании — передаётся
  `new(windows.Overlapped)`, заполнять поля вручную не требуется для этого сценария.
