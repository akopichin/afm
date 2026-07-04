# yaml.v3 (gopkg.in/yaml.v3)

Парсинг конфигурации (`.afm/config.yaml`) и описаний флоу (`flow.yaml`). Аудитория: клеточки `pkg/config`,
`pkg/flow`.

## Структурные теги и опциональные поля через указатель

Поля со струк-тегами `yaml:"name"` разбираются напрямую в Go-структуры через `yaml.Unmarshal(data, &target)`.
Опциональность (отличить "поле явно не задано" от zero-value) выражается указателем плюс методом-геттером
с дефолтом:

```go
type ServerConfig struct {
	Port        *int  `yaml:"port"`
	OpenBrowser *bool `yaml:"open_browser"`
}

func (s ServerConfig) GetPort() int {
	if s.Port == nil {
		return 9876
	}
	return *s.Port
}
```

```go
var overlay Config
if err := yaml.Unmarshal(data, &overlay); err != nil {
	return fmt.Errorf("parse config: %w", err)
}
```

## Кастомный UnmarshalYAML для полиморфного поля

Когда одно и то же YAML-поле в разных местах флоу задаётся то строкой, то объектом (`"stage.artifact"`
или `{ref: stage.artifact, optional: true}`), тип реализует `yaml.Unmarshaler`, проверяя `value.Kind`, и
делегирует остальной разбор через alias-тип (`type plain Input`), чтобы не зациклиться на своём же
`UnmarshalYAML`:

```go
type Input struct {
	Ref      string `yaml:"ref"`
	Optional bool   `yaml:"optional"`
}

func (inp *Input) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		inp.Ref = value.Value
		return nil
	}
	type plain Input
	return value.Decode((*plain)(inp))
}
```

## Особенности

- Проект использует только чтение (`Unmarshal`/кастомный `UnmarshalYAML`); маршалинг обратно в YAML
  (запись конфигов) в кодовой базе не встречается.
- Ошибки парсинга всегда оборачиваются через `fmt.Errorf("...: %w", err)` с контекстом (что именно
  парсили), а не пробрасываются как есть.
