package docker

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/config"
)

// FileRootsEnvVar — имя переменной окружения, через которую хост передаёт
// закодированный манифест file roots внутрь контейнера.
const FileRootsEnvVar = "AFM_DOCKER_FILE_ROOTS"

// fileRootManifestVersion — версия формата FileRootManifest. Меняется при
// несовместимых изменениях схемы; DecodeFileRootManifest отвергает всё, что
// не совпадает с текущей версией.
const fileRootManifestVersion = 1

// FileRootManifestEntry — один просматриваемый в file browser корень:
// либо сам проект, либо один browse:true extra_mounts.
type FileRootManifestEntry struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	ContainerPath string `json:"container_path"`
	MountReadOnly bool   `json:"mount_read_only"`
	Kind          string `json:"kind"` // "project" | "extra"
}

// FileRootManifest — версионированный список корней file browser,
// передаваемый из хост-лаунчера в контейнер через FileRootsEnvVar.
type FileRootManifest struct {
	Version int                     `json:"version"`
	Roots   []FileRootManifestEntry `json:"roots"`
}

// BuildFileRootManifest строит манифест: корень проекта всегда первым
// (id:"project", read-write), плюс по одному extra-N на каждый browse:true
// mount (read-only). Пути без browse:true (в т.ч. legacy-строковая форма)
// не попадают в манифест — они существуют в контейнере (на случай если
// агенту нужен доступ), но не показываются в file browser.
//
// Путь mount'а с ведущим "~" разворачивается относительно домашнего каталога
// контейнера (containerHome); прочий относительный путь — относительно
// projectContainerPath (так же, как он был бы разрешён на хосте относительно
// рабочей директории проекта).
func BuildFileRootManifest(projectContainerPath string, mounts config.ExtraMounts) (FileRootManifest, error) {
	m := FileRootManifest{
		Version: fileRootManifestVersion,
		Roots: []FileRootManifestEntry{{
			ID:            "project",
			Label:         filepath.Base(projectContainerPath),
			ContainerPath: projectContainerPath,
			MountReadOnly: false,
			Kind:          "project",
		}},
	}

	seen := map[string]bool{projectContainerPath: true}
	n := 0
	for _, em := range mounts {
		if !em.Browse {
			continue
		}
		container := containerPathFor(projectContainerPath, em.Path)
		label := em.Name
		if label == "" {
			label = filepath.Base(container)
		}
		if seen[container] && label == filepath.Base(container) {
			continue // дубликат без собственного имени — пропускаем
		}
		seen[container] = true
		n++
		m.Roots = append(m.Roots, FileRootManifestEntry{
			ID:            fmt.Sprintf("extra-%d", n),
			Label:         label,
			ContainerPath: container,
			MountReadOnly: true,
			Kind:          "extra",
		})
	}
	return m, nil
}

// containerPathFor резолвит путь extra_mounts в абсолютный путь внутри
// контейнера: "~/..." — относительно домашнего каталога контейнера,
// уже абсолютный путь — как есть, прочий относительный — относительно
// корня проекта в контейнере.
func containerPathFor(projectContainerPath, path string) string {
	if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~") {
		path = filepath.Join(projectContainerPath, path)
	}
	return expandHome(path, containerHome)
}

// EncodeFileRootManifest сериализует манифест в JSON и кодирует его в
// base64 (без padding, URL-safe alphabet) для безопасной передачи через
// переменную окружения.
func EncodeFileRootManifest(m FileRootManifest) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// DecodeFileRootManifest декодирует и валидирует манифест: версия должна
// совпадать с текущей, id корней — быть уникальными, container_path — быть
// абсолютным.
func DecodeFileRootManifest(raw string) (FileRootManifest, error) {
	var m FileRootManifest
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return m, fmt.Errorf("decode file roots: %w", err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("parse file roots: %w", err)
	}
	if m.Version != fileRootManifestVersion {
		return m, fmt.Errorf("unsupported file roots version %d", m.Version)
	}
	ids := map[string]bool{}
	for _, r := range m.Roots {
		if ids[r.ID] {
			return m, fmt.Errorf("duplicate root id %q", r.ID)
		}
		ids[r.ID] = true
		if !filepath.IsAbs(r.ContainerPath) {
			return m, fmt.Errorf("root %q: container path not absolute: %q", r.ID, r.ContainerPath)
		}
	}
	return m, nil
}
