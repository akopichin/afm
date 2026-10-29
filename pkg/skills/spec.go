package skills

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Короткие YAML-теги, по которым строго отличаем строку от числа, bool и null.
const (
	tagStr  = "!!str"
	tagNull = "!!null"
)

// Names — обязательные скилы агента. В YAML это строка или список строк; повторы удаляются, порядок и
// регистр сохраняются, namespace-имена (superpowers:tdd) не меняются.
type Names []string

// UnmarshalYAML разбирает строку или список строк. Для null yaml.v3 его не вызывает — такие ключи
// отклоняет RejectNullKeys.
func (n *Names) UnmarshalYAML(value *yaml.Node) error {
	parsed, err := ParseNames(value)
	if err != nil {
		return err
	}
	*n = parsed
	return nil
}

// ParseNames разбирает узел skills строго: только !!str, не пустое имя; null, числа, объекты и смешанные
// списки — ошибка. Пустой список даёт пустой результат.
func ParseNames(value *yaml.Node) (Names, error) {
	value = resolveAlias(value)
	switch value.Kind {
	case yaml.ScalarNode:
		name, err := skillName(value)
		if err != nil {
			return nil, err
		}
		return Names{name}, nil
	case yaml.SequenceNode:
		out := make(Names, 0, len(value.Content))
		seen := make(map[string]bool, len(value.Content))
		for _, item := range value.Content {
			name, err := skillName(resolveAlias(item))
			if err != nil {
				return nil, err
			}
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
		return out, nil
	default:
		return nil, errors.New("skills must be a skill name or a list of skill names")
	}
}

func skillName(node *yaml.Node) (string, error) {
	if node.Kind != yaml.ScalarNode || node.ShortTag() != tagStr {
		return "", errors.New("skills: each skill name must be a string")
	}
	if strings.TrimSpace(node.Value) == "" {
		return "", errors.New("skills: skill name must not be empty")
	}
	if !validName(node.Value) {
		return "", errors.New("skills: skill name must not contain control characters")
	}
	return node.Value, nil
}

// validName отклоняет пустое имя и управляющие символы: имя попадает в промпт как строка каталога.
func validName(name string) bool {
	return strings.TrimSpace(name) != "" && !hasControlChars(name)
}

// SingleLine заменяет управляющие символы пробелами: значение остаётся одной строкой промпта, даже если
// проверка путей и имён что-то пропустила.
func SingleLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// hasControlChars сообщает об управляющих символах (в том числе переводе строки): строка с ними ломает
// построчный формат каталога в промпте.
func hasControlChars(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// Dir — путь к дополнительной локальной библиотеке скилов (skills_dir). Пустое значение означает «не
// задано»: явная пустая строка отклоняется при разборе, поэтому она не может превратиться в наследование.
type Dir string

// UnmarshalYAML разбирает путь; null yaml.v3 сюда не передаёт — см. RejectNullKeys.
func (d *Dir) UnmarshalYAML(value *yaml.Node) error {
	parsed, err := ParseDir(value)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// ParseDir разбирает узел skills_dir: строка !!str, не пустая и без `~` в начале (раскрытие домашнего
// каталога не поддерживается — для общей библиотеки задавайте абсолютный путь).
func ParseDir(value *yaml.Node) (Dir, error) {
	value = resolveAlias(value)
	if value.Kind != yaml.ScalarNode || value.ShortTag() != tagStr {
		return "", errors.New("skills_dir must be a string path")
	}
	if strings.TrimSpace(value.Value) == "" {
		return "", errors.New("skills_dir must not be empty; omit the key to inherit the outer setting")
	}
	if hasControlChars(value.Value) {
		return "", errors.New("skills_dir must not contain control characters")
	}
	if strings.HasPrefix(value.Value, "~") {
		return "", fmt.Errorf("skills_dir %q: \"~\" is not expanded; use an absolute or project-relative path", value.Value)
	}
	return Dir(value.Value), nil
}

// Resolve привязывает относительный путь к base; абсолютный возвращает очищенным. Пустой Dir остаётся пустым.
func (d Dir) Resolve(base string) string {
	switch {
	case d == "":
		return ""
	case filepath.IsAbs(string(d)):
		return filepath.Clean(string(d))
	default:
		return filepath.Join(base, string(d))
	}
}

// FirstDir возвращает ближайшее заданное значение из списка «от самого специфичного к самому общему»:
// библиотеки разных уровней не объединяются.
func FirstDir(dirs ...Dir) Dir {
	for _, d := range dirs {
		if d != "" {
			return d
		}
	}
	return ""
}

// RejectNullKeys отклоняет «key:» без значения (или null) в mapping-узле: yaml.v3 не вызывает UnmarshalYAML
// для null, и декодированная структура не отличила бы его от отсутствующего ключа.
func RejectNullKeys(mapping *yaml.Node, keys ...string) error {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := mapping.Content[i].Value
		for _, want := range keys {
			if key == want && mapping.Content[i+1].ShortTag() == tagNull {
				return fmt.Errorf("%s must not be empty or null; omit the key instead", key)
			}
		}
	}
	return nil
}

func resolveAlias(node *yaml.Node) *yaml.Node {
	for node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	return node
}
