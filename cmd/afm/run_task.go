package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/fsutil"
)

const (
	// maxTaskBytes ограничивает задачу: она целиком попадает в prompt каждого агента.
	maxTaskBytes     = 1 << 20
	taskFetchTimeout = 30 * time.Second
	maxTaskRedirects = 5
	// taskFileName хранит задачу в каталоге рана, чтобы resume и следующие циклы repeat работали без --task.
	taskFileName = "task.md"
)

// loadTask читает значение --task: текст как есть, @file, @- (stdin) или http(s)-URL.
func loadTask(ctx context.Context, value string, stdin io.Reader) (string, error) {
	var text string
	var err error
	switch {
	case value == "@-":
		text, err = readLimited(stdin)
	case strings.HasPrefix(value, "@"):
		text, err = readTaskFile(strings.TrimPrefix(value, "@"))
	case strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://"):
		text, err = fetchTask(ctx, value)
	default:
		text = value
		if len(text) > maxTaskBytes {
			err = errTaskTooLarge
		}
	}
	if err != nil {
		return "", fmt.Errorf("--task: %w", err)
	}
	if !utf8.ValidString(text) {
		return "", errors.New("--task: the task is not valid UTF-8")
	}
	// The text is kept byte for byte; only a task with nothing but whitespace is refused.
	if strings.TrimSpace(text) == "" {
		return "", errors.New("--task: the task is empty")
	}
	return text, nil
}

var errTaskTooLarge = fmt.Errorf("the task is larger than %d bytes", maxTaskBytes)

// readLimited читает не больше maxTaskBytes: лишний байт означает превышение лимита.
func readLimited(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxTaskBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxTaskBytes {
		return "", errTaskTooLarge
	}
	return string(data), nil
}

func readTaskFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the path is the user's own --task argument
	if err != nil {
		return "", fmt.Errorf("read task file: %w", err)
	}
	defer f.Close() //nolint:errcheck
	return readLimited(f)
}

// redirectRefusal — отказ checkTaskRedirect; его текст написан здесь и не содержит URL.
type redirectRefusal struct{ reason string }

func (e redirectRefusal) Error() string { return e.reason }

// fetchFailure сводит ошибку net/http к фиксированной категории: вложенный текст (разбор Location, dial) может
// содержать полный URL с токеном из query или учётными данными.
func fetchFailure(err error) string {
	const timedOut = "the request timed out"
	var refusal redirectRefusal
	switch {
	case errors.As(err, &refusal):
		return refusal.reason
	case errors.Is(err, context.Canceled):
		return "the request was canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return timedOut
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return timedOut
	}
	return "the request failed"
}

// fetchTask скачивает задачу по URL. Ошибки не содержат query: в ней часто лежит токен.
func fetchTask(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("the task URL is invalid")
	}
	if u.User != nil {
		return "", errors.New("the task URL must not contain credentials")
	}
	shown := maskedURL(u)

	client := &http.Client{Timeout: taskFetchTimeout, CheckRedirect: checkTaskRedirect}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("fetch %s: invalid request", shown)
	}
	resp, err := client.Do(req) //nolint:gosec // G107: fetching the user's own --task URL is the feature
	if err != nil {
		return "", fmt.Errorf("fetch %s: %s", shown, fetchFailure(err))
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: HTTP %d", shown, resp.StatusCode)
	}
	if mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || !strings.HasPrefix(mediaType, "text/") {
		return "", fmt.Errorf("fetch %s: the response is not text/*", shown)
	}
	text, err := readLimited(resp.Body)
	if err != nil && !errors.Is(err, errTaskTooLarge) {
		return "", fmt.Errorf("fetch %s: reading the response failed", shown)
	}
	return text, err
}

// checkTaskRedirect не даёт редиректу увести загрузку на другую схему или к учётным данным.
func checkTaskRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxTaskRedirects {
		return redirectRefusal{"too many redirects"}
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return redirectRefusal{fmt.Sprintf("redirect to a %q URL refused", req.URL.Scheme)}
	}
	if req.URL.User != nil {
		return redirectRefusal{"redirect to a URL with credentials refused"}
	}
	return nil
}

// maskedURL — URL для сообщений: без query и fragment.
func maskedURL(u *url.URL) string {
	shown := *u
	if shown.RawQuery != "" {
		shown.RawQuery = "..."
	}
	shown.Fragment = ""
	return shown.String()
}

func writeRunTask(runDir, task string) error {
	return fsutil.AtomicWriteFile(filepath.Join(runDir, taskFileName), []byte(task), 0o600)
}

// readRunTask возвращает задачу рана; ран без задачи — пустая строка без ошибки.
func readRunTask(runDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(runDir, taskFileName)) //nolint:gosec // G304: path under the run directory
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}

// promptWithTask дописывает задачу к промпту флоу, не меняя сам флоу.
func promptWithTask(prompt, task string) string {
	prompt = strings.TrimSpace(prompt)
	switch {
	case task == "":
		return prompt
	case prompt == "":
		return task
	}
	return prompt + "\n\n" + task
}

// prepareRunTask решает, какая задача у рана: явная записывается в его каталог, иначе берётся задача
// продолженного рана (пусто, если её нет).
func prepareRunTask(runDir, task string) (string, error) {
	if task == "" {
		return readRunTask(runDir)
	}
	return task, writeRunTask(runDir, task)
}

// writeTaskSnapshot кладёт задачу в файл внутри afm-root, который Docker монтирует по тому же пути: так контейнер
// получает задачу без текста в argv. Вызывающий удаляет файл.
func writeTaskSnapshot(afmRoot, task string) (string, error) {
	dir := filepath.Join(afmRoot, config.AfmDir, "tasks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create task directory: %w", err)
	}
	f, err := os.CreateTemp(dir, "task-*.md")
	if err != nil {
		return "", fmt.Errorf("create task snapshot: %w", err)
	}
	defer f.Close() //nolint:errcheck
	if _, err := f.WriteString(task); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write task snapshot: %w", err)
	}
	return f.Name(), nil
}

// withoutTaskArg убирает --task из аргументов командной строки: хост передаёт задачу контейнеру файлом.
func withoutTaskArg(args []string) []string {
	var kept []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--task":
			i++ // значение флага
		case strings.HasPrefix(args[i], "--task="):
		default:
			kept = append(kept, args[i])
		}
	}
	return kept
}
