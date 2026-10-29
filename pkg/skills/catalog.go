package skills

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/akopichin/afm/pkg/fsutil"
)

const (
	// skillFile — файл скила внутри его каталога.
	skillFile = "SKILL.md"
	// frontmatterFence ограничивает YAML-блок в начале SKILL.md.
	frontmatterFence = "---"
	// Лимиты каталога в промпте: превышение — ошибка, а не молчаливая обрезка.
	MaxCatalogEntries = 256
	MaxCatalogBytes   = 32 << 10
)

// Entry — один скил библиотеки. Path — точный путь к SKILL.md под каноническим корнем библиотеки.
type Entry struct {
	Name        string
	Description string
	Path        string
}

// Catalog — прочитанная библиотека: Root канонический, Entries отсортированы по имени.
type Catalog struct {
	Root    string
	Entries []Entry
}

// LoadCatalog читает дочерние каталоги dir со SKILL.md; скрытые «.x» пропускаются. Любая некорректная запись —
// ошибка (симлинк за канонический корень, дубликат имени, битый frontmatter, нет SKILL.md); внутренние
// симлинки допустимы.
func LoadCatalog(dir string) (Catalog, error) {
	root, err := fsutil.CanonicalPath(dir)
	if err != nil {
		return Catalog{}, fmt.Errorf("skills_dir %s: %w", dir, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return Catalog{}, fmt.Errorf("skills_dir %s: %w", dir, err)
	}
	if !info.IsDir() {
		return Catalog{}, fmt.Errorf("skills_dir %s: not a directory", dir)
	}
	if hasControlChars(root) {
		return Catalog{}, fmt.Errorf("skills_dir %q: the path must not contain control characters", root)
	}
	children, err := os.ReadDir(root)
	if err != nil {
		return Catalog{}, fmt.Errorf("skills_dir %s: %w", dir, err)
	}

	var entries []Entry
	byName := map[string]string{}
	for _, child := range children {
		// Ссылка на корне библиотеки (в том числе скрытая и на обычный файл) тоже не должна вести наружу.
		if child.Type()&fs.ModeSymlink != 0 {
			if _, err := checkSymlinkInside(root, filepath.Join(root, child.Name())); err != nil {
				return Catalog{}, err
			}
		}
		if strings.HasPrefix(child.Name(), ".") {
			continue
		}
		entry, ok, err := loadEntry(root, child.Name())
		if err != nil {
			return Catalog{}, err
		}
		if !ok {
			continue
		}
		if prev, dup := byName[entry.Name]; dup {
			return Catalog{}, fmt.Errorf("skills_dir %s: duplicate skill name %q in %s and %s", root, entry.Name, prev, entry.Path)
		}
		byName[entry.Name] = entry.Path
		entries = append(entries, entry)
	}
	if len(entries) > MaxCatalogEntries {
		return Catalog{}, fmt.Errorf("skills_dir %s: %d skills exceed the limit of %d", root, len(entries), MaxCatalogEntries)
	}
	slices.SortFunc(entries, func(a, b Entry) int { return cmp.Compare(a.Name, b.Name) })
	return Catalog{Root: root, Entries: entries}, nil
}

// loadEntry читает скил из root/dirName; ok=false — запись не каталог (обычный файл рядом с каталогами).
func loadEntry(root, dirName string) (entry Entry, ok bool, err error) {
	dirPath := filepath.Join(root, dirName)
	info, err := os.Stat(dirPath)
	if err != nil {
		return Entry{}, false, fmt.Errorf("skills_dir entry %s: %w", dirPath, err)
	}
	if !info.IsDir() {
		return Entry{}, false, nil
	}
	if hasControlChars(dirName) {
		return Entry{}, false, fmt.Errorf("skills_dir entry %q: the directory name must not contain control characters", dirPath)
	}
	realDir, err := filepath.EvalSymlinks(dirPath)
	if err != nil {
		return Entry{}, false, fmt.Errorf("skills_dir entry %s: %w", dirPath, err)
	}
	if !isInside(root, realDir) {
		return Entry{}, false, fmt.Errorf("skills_dir entry %s resolves to %s outside the library %s", dirPath, realDir, root)
	}

	skillPath := filepath.Join(dirPath, skillFile)
	fileInfo, err := os.Stat(skillPath)
	if errors.Is(err, fs.ErrNotExist) {
		return Entry{}, false, fmt.Errorf("skills_dir entry %s has no %s", dirPath, skillFile)
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("skills_dir entry %s: %w", skillPath, err)
	}
	if !fileInfo.Mode().IsRegular() {
		return Entry{}, false, fmt.Errorf("skills_dir entry %s is not a regular file", skillPath)
	}
	if err := checkSymlinksInside(root, realDir, map[string]bool{}); err != nil {
		return Entry{}, false, err
	}

	data, err := os.ReadFile(skillPath)
	if err != nil {
		return Entry{}, false, fmt.Errorf("skills_dir entry %s: %w", skillPath, err)
	}
	name, description, err := parseFrontmatter(data, dirName)
	if err == nil && !validName(name) {
		err = fmt.Errorf("invalid skill name %q: it must be non-empty and have no control characters", name)
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("skills_dir entry %s: %w", skillPath, err)
	}
	return Entry{Name: name, Description: description, Path: skillPath}, true, nil
}

// checkSymlinksInside отклоняет симлинки под dir за корнем библиотеки или битые: их цель может быть
// недоступна в контейнере. Цели-каталоги обходятся тоже (visited защищает от циклов): за ссылкой на
// скрытый каталог библиотеки может лежать ссылка наружу.
func checkSymlinksInside(root, dir string, visited map[string]bool) error {
	if visited[dir] {
		return nil
	}
	visited[dir] = true
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("skills_dir %s: %w", path, err)
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		target, err := checkSymlinkInside(root, path)
		if err != nil {
			return err
		}
		if info, err := os.Stat(target); err == nil && info.IsDir() {
			return checkSymlinksInside(root, target, visited)
		}
		return nil
	})
}

// checkSymlinkInside возвращает цель симлинка path; битая ссылка или цель за корнем библиотеки — ошибка.
func checkSymlinkInside(root, path string) (target string, err error) {
	target, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("skills_dir symlink %s: %w", path, err)
	}
	if !isInside(root, target) {
		return "", fmt.Errorf("skills_dir symlink %s points to %s outside the library %s", path, target, root)
	}
	return target, nil
}

// isInside сообщает, лежит ли path в root или совпадает с ним; оба пути канонические.
func isInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && filepath.IsLocal(rel)
}

// parseFrontmatter берёт name и description из блока `---` в начале SKILL.md; без блока name — имя каталога,
// description пуст. Незакрытый блок, невалидный YAML и нестроковые значения — ошибка.
func parseFrontmatter(data []byte, dirName string) (name, description string, err error) {
	block, found, err := frontmatterBlock(data)
	if err != nil {
		return "", "", err
	}
	if !found {
		return dirName, "", nil
	}
	var meta map[string]yaml.Node
	if err := yaml.Unmarshal(block, &meta); err != nil {
		return "", "", fmt.Errorf("invalid frontmatter: %w", err)
	}
	name = dirName
	if node, ok := meta["name"]; ok {
		if name, err = frontmatterString(node, "name"); err != nil {
			return "", "", err
		}
		if !validName(name) {
			return "", "", errors.New("invalid frontmatter: name must be a non-empty single-line string")
		}
	}
	if node, ok := meta["description"]; ok {
		if description, err = frontmatterString(node, "description"); err != nil {
			return "", "", err
		}
		description = strings.Join(strings.Fields(description), " ")
	}
	return name, description, nil
}

// frontmatterString требует !!str; пустое значение возвращается как "" (null считается отсутствием значения).
func frontmatterString(node yaml.Node, key string) (string, error) {
	if node.ShortTag() == tagNull {
		return "", nil
	}
	if node.ShortTag() != tagStr {
		return "", fmt.Errorf("invalid frontmatter: %s must be a string", key)
	}
	return strings.TrimSpace(node.Value), nil
}

// frontmatterBlock возвращает текст между первой строкой `---` и следующей строкой `---`.
func frontmatterBlock(data []byte) (block []byte, found bool, err error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) == 0 || string(bytes.TrimRight(lines[0], "\r")) != frontmatterFence {
		return nil, false, nil
	}
	for i := 1; i < len(lines); i++ {
		if string(bytes.TrimRight(lines[i], "\r")) == frontmatterFence {
			return bytes.Join(lines[1:i], []byte("\n")), true, nil
		}
	}
	return nil, false, errors.New("invalid frontmatter: closing --- not found")
}

// Block форматирует каталог для промпта по строке на скил (управляющие символы заменяются пробелами);
// больше MaxCatalogBytes — ошибка. Экранирует значения вызывающий промпт.
func (c Catalog) Block() (string, error) {
	var sb strings.Builder
	for _, e := range c.Entries {
		fmt.Fprintf(&sb, "- %s: %s (%s)\n", SingleLine(e.Name), SingleLine(e.Description), SingleLine(e.Path))
	}
	if sb.Len() > MaxCatalogBytes {
		return "", fmt.Errorf("skills_dir %s: catalog is %d bytes, limit is %d", c.Root, sb.Len(), MaxCatalogBytes)
	}
	return sb.String(), nil
}
