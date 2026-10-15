package assistants

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// DepthSource называет слой, из которого взято итоговое значение max_depth.
type DepthSource string

const (
	DepthSourceDefault DepthSource = "default"
	DepthSourceGlobal  DepthSource = "global"
	DepthSourceProject DepthSource = "project"
	DepthSourceFlow    DepthSource = "flow"
)

// defaultMaxDepth — глубина делегирования, если ни один слой конфигурации её
// не задал.
const defaultMaxDepth = 3

// DepthPolicy — разрешённое значение max_depth вместе с тем, откуда оно взято,
// и стабильным хешем входов (пишется в журнал, чтобы задачи, созданные под
// старой политикой, были отличимы от созданных под новой).
type DepthPolicy struct {
	MaxDepth int
	Source   DepthSource
	Revision string
}

// ResolveDepth разрешает max_depth по приоритету flow > project > global >
// дефолт (3). Это НЕ min(): более специфичный слой может как понизить, так и
// повысить значение более общего — global/project задают дефолт, а не
// административный потолок. nil означает «слой не настроен»; явный 0 в любом
// слое означает «помощников не создавать» и не наследуется от менее
// специфичных слоёв.
func ResolveDepth(global, project, flow *int) (DepthPolicy, error) {
	for _, v := range []*int{global, project, flow} {
		if v != nil && *v < 0 {
			return DepthPolicy{}, fmt.Errorf("assistants: max_depth must be >= 0, got %d", *v)
		}
	}

	maxDepth := defaultMaxDepth
	source := DepthSourceDefault
	switch {
	case flow != nil:
		maxDepth, source = *flow, DepthSourceFlow
	case project != nil:
		maxDepth, source = *project, DepthSourceProject
	case global != nil:
		maxDepth, source = *global, DepthSourceGlobal
	default:
		// ни один слой не настроен — остаётся дефолт (defaultMaxDepth, DepthSourceDefault).
	}

	return DepthPolicy{
		MaxDepth: maxDepth,
		Source:   source,
		Revision: depthRevision(global, project, flow),
	}, nil
}

// depthRevision — стабильный хеш входов ResolveDepth. Намеренно детерминирован
// и не зависит от времени/рандома: одни и те же входы на любом процессе дают
// одну и ту же ревизию.
func depthRevision(global, project, flow *int) string {
	format := func(v *int) string {
		if v == nil {
			return "nil"
		}
		return fmt.Sprintf("%d", *v)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("v1|global=%s|project=%s|flow=%s",
		format(global), format(project), format(flow))))
	return hex.EncodeToString(sum[:])
}
