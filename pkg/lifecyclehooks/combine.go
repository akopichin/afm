package lifecyclehooks

// Layer — слой хуков с опциональным скоупом стадии.
type Layer struct {
	StageID string
	Hooks   []Hook
}

// Combine складывает слои в итоговый список. Новые id дописываются в порядке появления.
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
