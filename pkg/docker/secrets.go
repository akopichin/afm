package docker

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/config"
)

// homeDir возвращает домашний каталог хоста (best-effort).
// Используется для раскрытия ~ в путях к секретам и system_prompt на хосте.
func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// LoadSecrets парсит один или несколько файлов вида KEY=VALUE (позже — выше
// приоритет) в map. Отсутствующие файлы игнорируются. Пустые строки и строки
// комментариев '#' пропускаются. Пути с ~ раскрываются относительно домашнего
// каталога хоста.
func LoadSecrets(paths []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range paths {
		f, err := os.Open(expandHome(p, homeDir()))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("open secrets %s: %w", p, err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if k, v, ok := strings.Cut(line, "="); ok {
				out[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read secrets %s: %w", p, err)
		}
	}
	return out, nil
}

// ResolveAuthValue резолвит ссылку auth.from ("env:VAR" | "file:PATH")
// относительно загруженного map секретов (env: сначала map, затем os.Getenv)
// или чтением файла (file:). ~ раскрывается относительно домашнего каталога хоста.
func ResolveAuthValue(from string, secrets map[string]string) (string, error) {
	switch {
	case strings.HasPrefix(from, "env:"):
		key := strings.TrimPrefix(from, "env:")
		if v, ok := secrets[key]; ok && v != "" {
			return v, nil
		}
		if v := os.Getenv(key); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("secret not found: env:%s (checked secrets file and process env)", key)
	case strings.HasPrefix(from, "file:"):
		path := expandHome(strings.TrimPrefix(from, "file:"), homeDir())
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("secret not found: %s: %w", from, err)
		}
		if v := strings.TrimSpace(string(data)); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("secret file is empty: %s", path)
	default:
		return "", fmt.Errorf("auth.from must be env:VAR or file:PATH, got %q", from)
	}
}

// ResolveSystemPrompt читает содержимое ссылки system_prompt вида "file:PATH".
// Пустая ссылка → ("", nil) (sysprompt не задан). ~ раскрывается относительно
// домашнего каталога хоста.
func ResolveSystemPrompt(ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	if !strings.HasPrefix(ref, "file:") {
		return "", fmt.Errorf("system_prompt must be file:PATH, got %q", ref)
	}
	path := expandHome(strings.TrimPrefix(ref, "file:"), homeDir())
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("system_prompt not found: %s: %w", path, err)
	}
	return string(data), nil
}

// LoadSecretLayers собирает map секретов из слоёв по умолчанию (глобальный
// ~/.afm/secrets.env + проектный <projectDir>/.afm/secrets.env) либо из файла
// override, если он задан. Вызывается только на хосте. Exported, чтобы внешний
// тестовый пакет docker_test мог обращаться к функции напрямую.
func LoadSecretLayers(override, projectDir string) (map[string]string, error) {
	var files []string
	if override != "" {
		files = []string{override}
	} else {
		files = []string{
			filepath.Join(homeDir(), config.AfmDir, "secrets.env"),
			filepath.Join(projectDir, config.AfmDir, "secrets.env"),
		}
	}
	return LoadSecrets(files)
}
