package docker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/secrets"
)

// homeDir возвращает домашний каталог хоста (best-effort).
// Используется для раскрытия ~ в путях к секретам и system_prompt на хосте.
func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
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
	path := secrets.ExpandHome(strings.TrimPrefix(ref, "file:"), homeDir())
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
	return secrets.LoadSecrets(files)
}
