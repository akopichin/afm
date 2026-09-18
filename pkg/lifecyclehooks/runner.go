package lifecyclehooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// DefaultHookTimeout применяется, когда h.Timeout == 0. 30s: уведомлящие
// скрипты короткие; 5 минут (как у script-стадий) — слишком щедро для observer.
const DefaultHookTimeout = 30 * time.Second

// hookRetryBackoff — пауза между попытками одного delivery.
const hookRetryBackoff = time.Second

// buildEnv формирует окружение hook-процесса (spec-дефолт — минимальное
// окружение, НЕ полное наследование): InheritEnv==false (дефолт) →
// minimalBaseEnv(); InheritEnv==true → stripTransportVars(os.Environ()).
// Затем всегда + afmVars(cfg,p) + h.ResolvedEnv последними (перекрывают
// базовые/AFM_*).
func buildEnv(h Hook, cfg DispatcherConfig, p Payload) []string {
	var env []string
	if h.InheritEnv {
		env = stripTransportVars(os.Environ()) // полное наследование, но без transport-переменных (codex #2)
	} else {
		env = minimalBaseEnv() // spec-дефолт: минимальное окружение (whitelist — transport не тащится)
	}
	env = append(env, afmVars(cfg, p)...) // AFM_*
	for k, v := range h.ResolvedEnv {
		env = append(env, k+"="+v) // резолвнутые секреты — последними (перекрывают)
	}
	return env
}

// afmVars — скалярные AFM_* переменные события/рана. Для flow-событий
// стадийные переменные пустые (контракт спеки).
func afmVars(cfg DispatcherConfig, p Payload) []string {
	var env []string
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

// minimalBaseEnv — spec-дефолт окружения hook-процесса: whitelist PATH/HOME/
// locale/TMPDIR/proxy/certs из os.Environ(). Transport-переменные (AFM_*
// секреты/sysprompt autoShim) в whitelist не входят по построению.
func minimalBaseEnv() []string {
	keys := []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR"}
	var out []string
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// stripTransportVars убирает внутренние transport-переменные из унаследованного
// окружения (codex #2): секреты хуков/agent-shim не должны течь в hook-процесс.
func stripTransportVars(env []string) []string {
	out := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "AFM_HOOK_SECRET_") || strings.HasPrefix(kv, "AFM_SECRET_") || strings.HasPrefix(kv, "AFM_SYSPROMPT_") {
			continue
		}
		out = append(out, kv)
	}
	return out
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
	cmd.Env = buildEnv(h, cfg, p)
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
	// Если у хука есть резолвнутые секреты — лог оборачивается редактором
	// (codex #5): секрет не должен попасть в лог хука ни через stdout/stderr
	// самой команды, ни через finish-строку.
	logFile, logErr := openAttemptLog(logPath, p, attempt)
	var sink io.Writer = logFile
	var redactor *redactingWriter
	if logErr == nil {
		if vals := secretValues(h); len(vals) > 0 {
			redactor = newRedactingWriter(logFile, vals)
			sink = redactor
		}
		cmd.Stdout = sink
		cmd.Stderr = sink
	} else {
		log.Printf("WARN: lifecycle hook log %s: %v", logPath, logErr)
	}
	err = cmd.Run()
	if logErr == nil {
		// Best-effort финальная строка: диск мог отвалиться посреди попытки.
		// Идёт через sink (редактор при наличии секретов) — err может нести
		// секрет из stderr агента.
		_, _ = fmt.Fprintf(sink, "=== attempt %d finished: %v ===\n", attempt, err)
		if redactor != nil {
			redactor.Close() // флаш буферизованного хвоста в logFile
		}
		logFile.Close()
	}
	// Санитизировать ошибку ДО возврата (она уходит в OnError → notices.jsonl).
	if err != nil {
		err = errors.New(redactString(err.Error(), secretValues(h)))
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
