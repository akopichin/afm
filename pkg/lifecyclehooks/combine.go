package lifecyclehooks

// Layer — слой хуков с опциональным скоупом стадии. Слои передаются в
// Combine в порядке возрастания специфичности: global config, project config
// (эти два уже смёржены в config.LoadFrom), flow, затем по одному слою на
// стадию.
type Layer struct {
	StageID string
	Hooks   []Hook
}

// Combine складывает слои в итоговый список. id — плоский namespace: хук с
// тем же id из более позднего (специфичного) слоя ЗАМЕНЯЕТ ранний целиком,
// включая скоуп стадии, на исходной позиции (порядок стабильный). Новые id
// дописываются в порядке появления.
func Combine(layers ...Layer) []RegisteredHook {
	var out []RegisteredHook
	idx := map[string]int{}
	for _, l := range layers {
		for _, h := range l.Hooks {
			if i, ok := idx[h.ID]; ok {
				out[i] = RegisteredHook{Hook: h, StageID: l.StageID}
				continue
			}
			idx[h.ID] = len(out)
			out = append(out, RegisteredHook{Hook: h, StageID: l.StageID})
		}
	}
	return out
}
