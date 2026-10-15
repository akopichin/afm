package server

import (
	"encoding/json"
	"net/http"

	"github.com/akopichin/afm/pkg/assistants"
)

// AssistantProfilesService — узкий интерфейс реестра помощников, потребляемый сервером вместо конкретного
// *assistants.Registry (мирроит SideAgentService). nil = capability off: маршрут /api/assistant-profiles не
// регистрируется, GET на него отдаёт 404 — тот же паттерн, что у side-agent (server.go).
type AssistantProfilesService interface {
	// Profiles возвращает текущий список профилей с их доступностью (assistants.Registry.List()).
	Profiles() []assistants.ProfileView
	// MaxDepth возвращает разрешённую глубину делегирования и слой, из которого она взята
	// ("flow"/"project"/"global"/"default" — assistants.DepthSource).
	MaxDepth() (depth int, source string)
}

// assistantProfileView — форма одного профиля в ответе GET /api/assistant-profiles. Command — это ИМЯ уже
// настроенной команды/recipe (например "claude", "codex-as-claude", "frontend-reviewer"), не путь на диске
// и не секрет — профиль не несёт ни токенов, ни абсолютных путей.
type assistantProfileView struct {
	ID                string `json:"id"`
	Title             string `json:"title"`
	Command           string `json:"command"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// assistantProfilesResponse — форма ответа GET /api/assistant-profiles.
type assistantProfilesResponse struct {
	Profiles       []assistantProfileView `json:"profiles"`
	MaxDepth       int                    `json:"max_depth"`
	MaxDepthSource string                 `json:"max_depth_source"`
}

// handleAssistantProfiles отдаёт список настроенных профилей помощников и эффективную глубину
// делегирования. Только чтение: клиент передаёт лишь profile_id при создании задачи (T3.x) — здесь нет
// кода, создающего профили из HTTP-запроса.
func (s *Server) handleAssistantProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	views := s.assistantProfiles.Profiles()
	resp := assistantProfilesResponse{Profiles: make([]assistantProfileView, 0, len(views))}
	for _, v := range views {
		resp.Profiles = append(resp.Profiles, assistantProfileView{
			ID:                string(v.ID),
			Title:             v.Title,
			Command:           v.Command,
			Available:         v.Available,
			UnavailableReason: v.UnavailableReason,
		})
	}
	resp.MaxDepth, resp.MaxDepthSource = s.assistantProfiles.MaxDepth()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
