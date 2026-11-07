package lifecyclehooks

// Matches сообщает, должен ли хук сработать для события ev: вход в selector
// минус skip_events.
func (h Hook) Matches(ev EventType) bool {
	in := h.Events.All || containsEvent(h.Events.Events, ev)
	return in && !containsEvent(h.SkipEvents, ev)
}

// MatchesEvent — scope-матчинг: StageID "" получает события всех стадий,
// непустой — только своей.
func (rh RegisteredHook) MatchesEvent(ev Event) bool {
	return (rh.StageID == "" || rh.StageID == ev.StageID) && rh.Hook.Matches(ev.Type)
}

func containsEvent(list []EventType, ev EventType) bool {
	for _, e := range list {
		if e == ev {
			return true
		}
	}
	return false
}
