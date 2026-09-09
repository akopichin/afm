package memorypipeline

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// maxDatasetBytes ограничивает размер reflect_dataset.yaml, который читает
// код afm (не только LLM-агент, который его пишет) — защита от случайно
// раздутого/зациклившегося вывода агента, а не лимит протокола.
const maxDatasetBytes = 10 << 20

// Допустимые значения DatasetItem.Source — именованные константы, а не
// разбросанные строковые литералы (goconst).
const (
	sourceUser = "user"
	sourceLog  = "log"
)

// DatasetItem — один DPO-style элемент RL-датасета, который пишет
// reflect-агент: пара chosen/rejected для заданного prompt, с пометкой
// источника (review #7 в специфике pipeline — user-ввод приоритетнее лога).
type DatasetItem struct {
	Prompt   string
	Chosen   string
	Rejected string
	Source   string // "user" | "log"
}

// Dataset — разобранный reflect_dataset.yaml: project_level (переносится в
// общую memory.md) и session_level (переносится только в файл собственной
// стадии, если у неё есть reflect: write).
type Dataset struct {
	ProjectLevel []DatasetItem
	SessionLevel []DatasetItem
}

// datasetItemYAML — промежуточная форма для строгого декодирования одного
// элемента датасета (KnownFields(true) ловит опечатки/лишние поля).
type datasetItemYAML struct {
	Prompt   string `yaml:"prompt"`
	Chosen   string `yaml:"chosen"`
	Rejected string `yaml:"rejected"`
	Source   string `yaml:"source"`
}

// datasetYAML — строгая форма верхнего уровня для второго прохода
// декодирования (после того как форма документа уже проверена вручную через
// yaml.Node — см. ParseDataset).
type datasetYAML struct {
	ProjectLevel []datasetItemYAML `yaml:"project_level"`
	SessionLevel []datasetItemYAML `yaml:"session_level"`
}

// ParseDataset разбирает reflect_dataset.yaml со строгими требованиями к
// форме документа (review #10): ровно один YAML-документ, корень — mapping
// (не голый скаляр), присутствуют ОБА ключа project_level и session_level,
// никаких прочих ключей верхнего уровня. Использует yaml.Node для проверки
// формы документа (KnownFields(true) сам по себе не отклоняет ни голый
// скаляр, ни документ с пропущенной секцией), затем отдельный строгий
// проход для полей элементов.
func ParseDataset(data []byte) (Dataset, error) {
	if len(data) > maxDatasetBytes {
		return Dataset{}, fmt.Errorf("dataset too large: %d bytes (limit %d)", len(data), maxDatasetBytes)
	}

	var root yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&root); err != nil {
		return Dataset{}, fmt.Errorf("parse dataset: %w", err)
	}
	if err := dec.Decode(new(yaml.Node)); err != io.EOF {
		return Dataset{}, errors.New("dataset must contain exactly one YAML document")
	}

	doc := &root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return Dataset{}, errors.New("dataset root must be a mapping with project_level/session_level keys")
	}

	seen := map[string]bool{}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key := doc.Content[i].Value
		if key != "project_level" && key != "session_level" {
			return Dataset{}, fmt.Errorf("unknown top-level key %q", key)
		}
		seen[key] = true
	}
	if !seen["project_level"] || !seen["session_level"] {
		return Dataset{}, errors.New("dataset must have both project_level and session_level keys")
	}

	var ds datasetYAML
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	if err := strict.Decode(&ds); err != nil {
		return Dataset{}, fmt.Errorf("parse dataset items: %w", err)
	}

	return Dataset{
		ProjectLevel: convertDatasetItems(ds.ProjectLevel),
		SessionLevel: convertDatasetItems(ds.SessionLevel),
	}, nil
}

func convertDatasetItems(in []datasetItemYAML) []DatasetItem {
	out := make([]DatasetItem, len(in))
	for i, it := range in {
		out[i] = DatasetItem(it)
	}
	return out
}

// ValidateDataset проверяет reflect_dataset.yaml как ParseDataset (форма
// документа), плюс поэлементные требования: prompt/chosen/rejected
// непустые после trim, source ∈ {user, log}. Обе секции могут быть пустыми
// (валидно) — стадия могла не дать ничего заслуживающего внимания.
func ValidateDataset(data []byte) error {
	ds, err := ParseDataset(data)
	if err != nil {
		return err
	}
	return errors.Join(
		checkDatasetSection("project_level", ds.ProjectLevel),
		checkDatasetSection("session_level", ds.SessionLevel),
	)
}

func checkDatasetSection(name string, items []DatasetItem) error {
	for i, it := range items {
		if strings.TrimSpace(it.Prompt) == "" || strings.TrimSpace(it.Chosen) == "" || strings.TrimSpace(it.Rejected) == "" {
			return fmt.Errorf("%s[%d]: prompt/chosen/rejected must be non-empty", name, i)
		}
		if it.Source != sourceUser && it.Source != sourceLog {
			return fmt.Errorf("%s[%d]: source must be user|log, got %q", name, i, it.Source)
		}
	}
	return nil
}
