# Дизайн: встраивание AFM-скилов в бинарь

**Дата:** 2026-07-01  
**Статус:** согласован

## Цель

Сделать бинарник `afm` независимым от ручной установки скилов через `install.sh`. AFM-скилы (`afm`, `afm-check`, `afm-init`, `afm-retry`, `afm-review`) встраиваются в бинарь и устанавливаются через команду `afm install-skills`.

Скилы из flow YAML (например `superpowers:tdd`) по-прежнему управляются пользователем и берутся из `~/.claude` стандартным образом.

## Архитектура

### 1. Embed в `assets/assets.go`

Добавляется вторая embed-директива рядом с существующей:

```go
//go:embed prompts
var FS embed.FS

//go:embed claude/skills
var SkillsFS embed.FS
```

`SkillsFS` экспортируется из пакета `assets`. Содержимое: пять директорий `claude/skills/<name>/SKILL.md`.

### 2. Команда `afm install-skills`

Новый файл `cmd/afm/install_skills.go`, cobra-команда с флагами:

| Флаг | По умолчанию | Назначение |
|------|-------------|------------|
| `--skills-dir` | `~/.claude/skills` | Путь назначения (переопределение для тестов) |
| `--force` | false | Перезаписать существующие файлы |

**Алгоритм:**
1. Раскрыть `--skills-dir` (`~` → `os.UserHomeDir`)
2. `fs.WalkDir(assets.SkillsFS, "claude/skills", ...)` — обойти все файлы
3. Для каждого `claude/skills/<name>/SKILL.md`:
   - Создать `<skills-dir>/<name>/` если не существует
   - Пропустить если `<skills-dir>/<name>/SKILL.md` уже существует и `--force` не задан
   - Записать файл (содержимое из embedded FS)
4. Вывести список: `✓ afm`, `✓ afm-check (skipped)`, и т.д.
5. Итоговая подсказка: `Use /afm, /afm-check etc. in Claude Code.`

Команда идемпотентна: повторный запуск без `--force` не трогает уже установленные файлы.

### 3. Обновление `install.sh`

Блок ручного копирования скилов (`for skill in .../assets/claude/skills/*/`) удаляется. Заменяется интерактивным вопросом после установки бинарника:

```bash
echo ""
read -p "Установить Claude-скиллы (/afm, /afm-check и др.) в ~/.claude/skills/? [Y/n] " answer
case "$answer" in
  [nN]*)
    echo "Пропущено. Установи позже: afm install-skills"
    ;;
  *)
    "$INSTALL_DIR/$BIN_NAME" install-skills
    ;;
esac
```

`install.sh` больше не знает о структуре `assets/claude/skills/` — эта ответственность полностью переходит к бинарнику.

## Затронутые файлы

| Файл | Изменение |
|------|-----------|
| `assets/assets.go` | Добавить `//go:embed claude/skills` + `var SkillsFS embed.FS` |
| `cmd/afm/install_skills.go` | Новый файл — cobra-команда `install-skills` |
| `cmd/afm/main.go` | Зарегистрировать `newInstallSkillsCmd()` |
| `install.sh` | Убрать ручное копирование, добавить интерактивный вопрос |

## Что не меняется

- Скилы из `stage.Skills` в flow YAML по-прежнему пишутся как `<skills>name</skills>` в промпт агента — пользователь управляет ими сам через `~/.claude`
- Промпты (`planning.md`, `implementation.md`, etc.) — без изменений
- Структура `assets/claude/skills/` в репозитории — без изменений

## Тестирование

- Unit-тест для `install-skills`: использовать `--skills-dir` с временной директорией, проверить что все 5 SKILL.md записаны с правильным содержимым
- Тест идемпотентности: повторный запуск без `--force` не перезаписывает файлы
- Тест `--force`: перезаписывает изменённые файлы
