// Package secrets содержит нейтральные (без зависимости на pkg/config или
// pkg/docker) хелперы резолва секретов: раскрытие "~" в путях, разбор файлов
// вида KEY=VALUE и резолв ссылок "env:VAR" | "file:PATH". Используется как
// docker agent-recipes (pkg/docker), так и lifecycle hooks (pkg/lifecyclehooks).
package secrets

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ExpandHome раскрывает ведущую "~" в абсолютный путь относительно home.
// "~" → home, "~/foo" → home+"/foo"; прочее (включая "~user/foo") возвращается
// как есть.
func ExpandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return home + p[1:] // p[1:] == "/…"
	}
	return p
}

// LoadSecrets парсит один или несколько файлов вида KEY=VALUE (позже — выше
// приоритет) в map. Отсутствующие файлы игнорируются. Пустые строки и строки
// комментариев '#' пропускаются. Пути с ~ раскрываются относительно
// домашнего каталога текущего процесса (os.UserHomeDir).
func LoadSecrets(paths []string) (map[string]string, error) {
	home, _ := os.UserHomeDir()
	out := map[string]string{}
	for _, p := range paths {
		f, err := os.Open(ExpandHome(p, home))
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

// LoadLayers — тонкая обёртка над LoadSecrets: собирает map секретов из
// заданных путей (позже в списке — выше приоритет).
func LoadLayers(paths ...string) (map[string]string, error) {
	return LoadSecrets(paths)
}

// ResolveRef резолвит ссылку вида "env:VAR" | "file:PATH" относительно
// загруженного map секретов (env: сначала map, затем os.Getenv) или чтением
// файла (file:). ~ раскрывается относительно домашнего каталога текущего
// процесса (os.UserHomeDir).
func ResolveRef(ref string, loaded map[string]string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "env:"):
		key := strings.TrimPrefix(ref, "env:")
		if v, ok := loaded[key]; ok && v != "" {
			return v, nil
		}
		if v := os.Getenv(key); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("secret not found: env:%s (checked secrets file and process env)", key)
	case strings.HasPrefix(ref, "file:"):
		home, _ := os.UserHomeDir()
		path := ExpandHome(strings.TrimPrefix(ref, "file:"), home)
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("secret not found: %s: %w", ref, err)
		}
		if v := strings.TrimSpace(string(data)); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("secret file is empty: %s", path)
	default:
		return "", fmt.Errorf("auth.from must be env:VAR or file:PATH, got %q", ref)
	}
}
