// Package skills устанавливает embedded-скиллы afm в хранилище ~/.afm/skills и ссылается на них
// относительными симлинками из папок скиллов Claude и Codex.
package skills

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/assets"
	"github.com/akopichin/afm/pkg/fsutil"
	"github.com/akopichin/afm/pkg/progress"
)

// Report — результат Sync: папки store и агентов, строки об изменениях и предупреждения для пользователя.
type Report struct {
	Store     string   // папка store
	AgentDirs []string // папки скиллов агентов, ссылающиеся на store
	Installed []string // записанные файлы store и созданные или исправленные ссылки
	Warnings  []string // пропущенные записи и подсказки
	Backups   []string // папки, перенесённые в backup при force
}

// Точки инъекции ошибок для тестов.
var (
	rename  = os.Rename
	symlink = os.Symlink
)

// Sync записывает embedded-скиллы в ~/.afm/skills и создаёт на них ссылки в ~/.claude/skills и
// $CODEX_HOME/skills (иначе ~/.codex/skills). Настоящие папки с именами скиллов заменяются ссылками
// только при force (с backup). Ошибки отдельных скиллов не прерывают остальные и возвращаются вместе.
func Sync(home string, force bool) (Report, error) {
	src, err := fs.Sub(assets.SkillsFS, "claude/skills")
	if err != nil {
		return Report{}, err
	}
	return syncFS(home, force, src)
}

func syncFS(home string, force bool, src fs.FS) (Report, error) {
	var report Report
	if home == "" || !filepath.IsAbs(home) {
		return report, fmt.Errorf("home directory must be an absolute path, got %q", home)
	}
	afmDir := filepath.Join(home, ".afm")
	store := filepath.Join(afmDir, "skills")
	agents, agentsErr := agentDirs(home)
	report.Store = store
	roots := []string{store}
	for _, a := range agents {
		report.AgentDirs = append(report.AgentDirs, a.dir)
		roots = append(roots, a.dir)
	}
	for _, dir := range []string{afmDir, store} {
		if err := checkRealDirOrAbsent(dir); err != nil {
			return report, err
		}
	}
	if err := os.MkdirAll(afmDir, 0o755); err != nil {
		return report, err
	}
	unlock, err := lock(filepath.Join(afmDir, "skills.lock"))
	if err != nil {
		return report, err
	}
	defer unlock()

	if err := os.MkdirAll(store, 0o755); err != nil {
		return report, err
	}
	entries, err := fs.ReadDir(src, ".")
	if err != nil {
		return report, err
	}
	var names []string
	errs := []error{agentsErr}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if err := writeSkill(store, e.Name(), src, &report); err != nil {
			errs = append(errs, fmt.Errorf("skill %s: %w", e.Name(), err))
			continue // ссылка на недописанный скилл не нужна
		}
		names = append(names, e.Name())
	}

	if err := checkDisjoint(roots...); err != nil {
		return report, errors.Join(append(errs, err)...)
	}
	storeReal, err := filepath.EvalSymlinks(store)
	if err != nil {
		return report, errors.Join(append(errs, err)...)
	}
	ready, err := makeAgentDirs(store, agents, &errs)
	if err != nil {
		return report, errors.Join(append(errs, err)...)
	}
	for _, a := range ready {
		if err := linkAgent(a, storeReal, names, force, &report); err != nil {
			errs = append(errs, err)
		}
	}
	return report, errors.Join(errs...)
}

// makeAgentDirs создаёт папки агентов до первой ссылки и сверяет каждую физически со store и уже
// созданными папками: строка видит не все имена, которые ФС считает одной папкой (регистр, NFC/NFD,
// ſ/s на APFS). Агент, чью папку не удалось создать, пропускается с ошибкой в errs.
func makeAgentDirs(store string, agents []agent, errs *[]error) ([]agent, error) {
	created := []string{store}
	overlap := func(dir string) error {
		for _, other := range created {
			if physicallyWithin(dir, other) || physicallyWithin(other, dir) {
				return fmt.Errorf("directories %s and %s overlap, nothing linked", dir, other)
			}
		}
		return nil
	}
	var ready []agent
	for _, a := range agents {
		if err := overlap(a.dir); err != nil { // до MkdirAll: не создавать папку внутри чужого корня
			return nil, err
		}
		if err := os.MkdirAll(a.dir, 0o755); err != nil {
			*errs = append(*errs, fmt.Errorf("%s: %w", a.name, err))
			continue
		}
		if err := overlap(a.dir); err != nil { // после: папка агента могла оказаться предком другого корня
			return nil, err
		}
		created = append(created, a.dir)
		ready = append(ready, a)
	}
	return ready, nil
}

// agent — агент и папка, из которой он читает скиллы.
type agent struct{ name, dir string }

// agentDirs — папки скиллов агентов: Claude в ~/.claude/skills, Codex в $CODEX_HOME/skills.
// Относительный CODEX_HOME — ошибка: иначе папки создались бы в текущей папке (проекте при afm run).
func agentDirs(home string) ([]agent, error) {
	agents := []agent{{"claude", filepath.Join(home, ".claude", "skills")}}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	if !filepath.IsAbs(codexHome) {
		return agents, fmt.Errorf("codex: CODEX_HOME must be an absolute path, got %q", codexHome)
	}
	return append(agents, agent{"codex", filepath.Join(codexHome, "skills")}), nil
}

// checkRealDirOrAbsent не даёт писать store через симлинк или поверх не-папки.
func checkRealDirOrAbsent(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s must be a real directory", dir)
	}
	return nil
}

// lock берёт межпроцессную блокировку sync. Lock-файл не заменяется и не удаляется.
func lock(path string) (unlock func(), err error) {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file, got %v", path, info.Mode().Type())
	}
	l, err := progress.NewLock(path)
	if err != nil {
		return nil, err
	}
	if err := l.Lock(); err != nil {
		return nil, err
	}
	return l.Unlock, nil
}

// writeSkill копирует файлы скилла name из src в store/name. Симлинки внутри store
// заменяются настоящими записями, файлы с совпадающим содержимым не переписываются.
func writeSkill(store, name string, src fs.FS, report *Report) error {
	return fs.WalkDir(src, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(store, filepath.FromSlash(p))
		if d.IsDir() {
			return ensureRealDir(dst)
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		written, err := writeIfChanged(dst, data)
		if written {
			report.Installed = append(report.Installed, "store: "+p)
		}
		return err
	})
}

func ensureRealDir(dir string) error {
	info, err := removeSymlink(dir)
	switch {
	case err != nil:
		return err
	case info == nil:
		return os.Mkdir(dir, 0o755)
	case info.IsDir():
		return nil
	default:
		return fmt.Errorf("%s is not a directory", dir)
	}
}

// writeIfChanged атомарно записывает data в path, если содержимое отличается.
// Содержимое читается только у обычного файла: FIFO и устройства дают ошибку без чтения.
func writeIfChanged(path string, data []byte) (written bool, err error) {
	info, err := removeSymlink(path)
	if err != nil {
		return false, err
	}
	if info != nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("%s is not a regular file (%v)", path, info.Mode().Type())
		}
		if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
			if info.Mode().Perm() == 0o644 {
				return false, nil
			}
			// Файл с правами 0600 могла оставить прежняя версия afm.
			return false, os.Chmod(path, 0o644)
		}
	}
	// Скиллы читают агенты, в том числе в контейнере, поэтому 0644.
	if err := fsutil.AtomicWriteFile(path, data, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// removeSymlink удаляет симлинк path, чтобы запись шла в настоящий файл или папку, а не по ссылке.
// Возвращает сведения о настоящей записи; nil — записи нет (или удалён симлинк).
func removeSymlink(path string) (fs.FileInfo, error) {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	case info.Mode()&fs.ModeSymlink != 0:
		return nil, os.Remove(path)
	default:
		return info, nil
	}
}

// within сообщает, совпадает ли канонический путь p с root или лежит внутри него, без учёта регистра:
// ФС macOS и Windows по умолчанию к нему нечувствительны. Нужна для осторожной проверки пересечения корней.
func within(p, root string) bool {
	p, root = strings.ToLower(p), strings.ToLower(root)
	if p == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) { // у корня ФС ("/", `C:\`) он уже есть
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(p, prefix)
}

// physicallyWithin сообщает, что root существует и p или один из существующих предков p — та же
// папка. Ловит совпадения, которые не видны по строке: например, NFC- и NFD-имена на APFS.
func physicallyWithin(p, root string) bool {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return false
	}
	for {
		if info, err := os.Stat(p); err == nil && os.SameFile(info, rootInfo) {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

// checkDisjoint требует, чтобы корни попарно не совпадали и не вкладывались друг в друга:
// иначе force мог бы унести в backup store или папку другого агента.
func checkDisjoint(roots ...string) error {
	canon := make([]string, len(roots))
	for i, root := range roots {
		c, err := fsutil.CanonicalPath(root)
		if err != nil {
			return err
		}
		canon[i] = c
	}
	for i := range canon {
		for j := i + 1; j < len(canon); j++ {
			a, b := canon[i], canon[j]
			if within(a, b) || within(b, a) || physicallyWithin(a, b) || physicallyWithin(b, a) {
				return fmt.Errorf("directories %s and %s overlap, nothing linked", roots[i], roots[j])
			}
		}
	}
	return nil
}

// linkAgent создаёт в папке агента ссылки на скиллы store.
func linkAgent(a agent, storeReal string, names []string, force bool, report *Report) error {
	agentReal, err := filepath.EvalSymlinks(a.dir)
	if err != nil {
		return fmt.Errorf("%s: %w", a.name, err)
	}
	var errs []error
	for _, name := range names {
		if err := linkSkill(a, agentReal, storeReal, name, force, report); err != nil {
			errs = append(errs, fmt.Errorf("%s: %s: %w", a.name, filepath.Join(a.dir, name), err))
		}
	}
	return errors.Join(errs...)
}

// linkSkill приводит запись agentReal/name к относительной ссылке на store/name, не трогая чужие записи.
// Пользователю показывается путь через a.dir, а не раскрытый agentReal.
func linkSkill(a agent, agentReal, storeReal, name string, force bool, report *Report) error {
	entry := filepath.Join(agentReal, name)
	rel, err := filepath.Rel(agentReal, filepath.Join(storeReal, name))
	if err != nil {
		return err
	}
	warn := func(format string, args ...any) {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("%s: %s: ", a.name, filepath.Join(a.dir, name))+fmt.Sprintf(format, args...))
	}

	info, err := os.Lstat(entry)
	moveToBackup := false
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(entry)
		if err != nil {
			return err
		}
		if !pointsInto(agentReal, target, storeReal) {
			warn("not an afm link (-> %s), left untouched", target)
			return nil
		}
		if target == rel {
			return nil
		}
	case info.IsDir():
		if !force {
			warn("real directory; `afm install-skills --force` replaces it with a link and moves the directory to a backup")
			return nil
		}
		moveToBackup = true
	default:
		warn("neither a directory nor a link, left untouched")
		return nil
	}

	backup, err := replaceWithLink(agentReal, name, rel, moveToBackup)
	if err != nil {
		return err
	}
	report.Installed = append(report.Installed, a.name+": "+name)
	if backup != "" {
		report.Backups = append(report.Backups, backup)
	}
	return nil
}

// pointsInto сообщает, ведёт ли цель ссылки из папки dir внутрь root (даже если цель не существует).
// Регистр учитывается: ссылку, ведущую в другой по регистру путь, лучше оставить как чужую, чем заменить.
func pointsInto(dir, target, root string) bool {
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	resolved, err := fsutil.CanonicalPath(target)
	if err != nil {
		return false
	}
	// Строка — с учётом регистра, чтобы на Linux не принять соседнюю .AFM за store; физическая
	// проверка узнаёт store, записанный иначе (регистр на APFS).
	return resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator)) ||
		physicallyWithin(resolved, root)
}

// replaceWithLink заменяет запись agentReal/name (отсутствующую, afm-ссылку или настоящую папку при moveToBackup)
// ссылкой на rel. Папка переносится в новый backup рядом с папкой агента и возвращается на место,
// если подменить запись не удалось. Возвращает путь backup.
func replaceWithLink(agentReal, name, rel string, moveToBackup bool) (backup string, err error) {
	entry := filepath.Join(agentReal, name)
	tmp := filepath.Join(agentReal, ".afm-tmp-"+name+"-"+rand.Text())
	if err := symlink(rel, tmp); err != nil {
		return "", fmt.Errorf("cannot create a symlink (on Windows enable Developer Mode): %w", err)
	}
	defer os.Remove(tmp) //nolint:errcheck // после успешного rename ссылки уже нет

	backupDir := ""
	if moveToBackup {
		backupDir, err = os.MkdirTemp(filepath.Dir(agentReal), ".afm-skills-backup-")
		if err != nil {
			return "", err
		}
		backup = filepath.Join(backupDir, name)
		if err := rename(entry, backup); err != nil {
			os.Remove(backupDir) //nolint:errcheck // пустая папка, остаток не мешает
			return "", fmt.Errorf("move to backup: %w", err)
		}
	} else if err := os.Remove(entry); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// Windows не переименовывает поверх ссылки на каталог, поэтому старую afm-ссылку удаляем заранее.
		return "", err
	}

	if err := rename(tmp, entry); err != nil {
		if backup == "" {
			return "", err
		}
		if restoreErr := rename(backup, entry); restoreErr != nil {
			return "", fmt.Errorf("%w; restoring the directory failed (%v), it is kept in %s", err, restoreErr, backup)
		}
		os.Remove(backupDir) //nolint:errcheck // пустая папка, остаток не мешает
		return "", err
	}
	return backup, nil
}
