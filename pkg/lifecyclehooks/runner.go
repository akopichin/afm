package lifecyclehooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// DefaultHookTimeout применяется, когда h.Timeout == 0. 30s: уведомлящие
// скрипты короткие; 5 минут (как у script-стадий) — слишком щедро для observer.
const DefaultHookTimeout = 30 * time.Second

// hookRetryBackoff — пауза между попытками одного delivery.
const hookRetryBackoff = time.Second

// buildEnv формирует окружение hook-процесса: Phase 1 — полное наследование
// окружения AFM плюс скалярные AFM_* события. Для flow-событий стадийные
// переменные пустые (контракт спеки).
func buildEnv(cfg DispatcherConfig, p Payload) []string {
	env := os.Environ()
	stage := p.Stage
	set := func(k, v string) { env = append(env, k+"="+v) }
	set("AFM_HOOK_EVENT", string(p.Event))
	set("AFM_HOOK_EVENT_ID", p.EventID)
	set("AFM_FLOW_NAME", p.Flow.Name)
	set("AFM_RUN_ID", p.Flow.RunID)
	set("AFM_RUN_DIR", p.Flow.RunDir)
	set("AFM_ROOT_DIR", p.Flow.RootDir)
	if stage != nil {
		set("AFM_STAGE_ID", stage.ID)
		set("AFM_STAGE_NAME", stage.Name)
		set("AFM_STAGE_FROM", stage.From)
		set("AFM_STAGE_TO", stage.To)
	} else {
		set("AFM_STAGE_ID", "")
		set("AFM_STAGE_NAME", "")
		set("AFM_STAGE_FROM", "")
		set("AFM_STAGE_TO", "")
	}
	return env
}

// runCommand выполняет команду хука: sh -c, stdin = JSON-payload, CWD =
// cfg.RootDir, stdout/stderr дописываются в logPath. Попытки: h.Retries+1,
// каждая ограничена timeout. Возвращает nil после первой успешной попытки,
// иначе — последнюю ошибку.
func runCommand(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error {
	attempts := h.Retries + 1
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		lastErr = execOne(ctx, h, cfg, p, logPath, attempt)
		if lastErr == nil {
			return nil
		}
		if attempt < attempts {
			select {
			case <-time.After(hookRetryBackoff):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return lastErr
}

func execOne(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string, attempt int) error {
	timeout := h.Timeout
	if timeout == 0 {
		timeout = DefaultHookTimeout
	}
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payloadJSON, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	cmd := exec.CommandContext(attemptCtx, "sh", "-c", h.Command)
	cmd.Dir = cfg.RootDir
	cmd.Stdin = bytes.NewReader(payloadJSON)
	cmd.Env = buildEnv(cfg, p)
	// Своя process group: таймаут-килл бьёт по группе (-pid), иначе внук
	// скрипта (sleep &) держал бы stdout-канал и Run висел до его конца —
	// тот же урок, что pkg/executor.killProcessGroup. Платформозависимые
	// части — в runner_unix.go/runner_windows.go.
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		killProcessGroup(cmd, syscall.SIGKILL)
		return nil
	}

	// Потоковая запись stdout/stderr прямо в лог-файл (codex MAJ#12): буфер
	// в памяти не ограничен, шумный хук за 30s-таймаута мог бы исчерпать
	// память и уронить afm — observer не должен иметь такой рычаг. Ошибки
	// открытия лога не влияют на доставку (best-effort): вывод уходит в никуда.
	logFile, logErr := openAttemptLog(logPath, p, attempt)
	if logErr == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	} else {
		log.Printf("WARN: lifecycle hook log %s: %v", logPath, logErr)
	}
	err = cmd.Run()
	if logFile != nil {
		// Best-effort финальная строка: диск мог отвалиться посреди попытки.
		_, _ = fmt.Fprintf(logFile, "=== attempt %d finished: %v ===\n", attempt, err)
		logFile.Close()
	}
	return err
}

// openAttemptLog создаёт/дописывает лог хука и пишет заголовок попытки.
// Один воркер на хук → записи сериализованы, мьютекс не нужен.
func openAttemptLog(logPath string, p Payload, attempt int) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	// Заголовок best-effort: если не записался — сам лог-файл открыт, вывод
	// попытки всё равно попадёт в него.
	_, _ = fmt.Fprintf(f, "=== %s event=%s id=%s stage=%s attempt=%d ===\n",
		time.Now().Format(time.RFC3339), p.Event, p.EventID, stageIDOrDash(p), attempt)
	return f, nil
}

func stageIDOrDash(p Payload) string {
	if p.Stage != nil && p.Stage.ID != "" {
		return p.Stage.ID
	}
	return "-"
}
