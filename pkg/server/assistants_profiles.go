package server

import (
	"encoding/json"
	"net/http"

	"github.com/akopichin/afm/contracts/assistants"
)

// AssistantProfilesService — узкий интерфейс реестра помощников, потребляемый сервером вместо конкретного
// *assistants.Registry (мирроит SideAgentService). nil = capability off: маршрут /api/assistant-profiles не
// регистрируется, GET на него отдаёт 404 — тот же паттерн, что у side-agent (server.go).
type AssistantProfilesService interface {
	// Profiles возвращает профили такими, какими они запустятся у стадии stageID, а при пустом stageID — у агента
	// рана. Неизвестная стадия — отказ assistants.CodeNotFound, script-стадия — assistants.CodeInvalidRequest.
	Profiles(stageID string) ([]assistants.ProfileView, error)
	// MaxDepth возвращает разрешённую глубину делегирования и слой, из которого она взята
	// ("flow"/"project"/"global"/"default" — assistants.DepthSource).
	MaxDepth() (depth int, source string)
}

// assistantProfileView — форма одного профиля в ответе GET /api/assistant-profiles. Command — команда из
// конфига (имя recipe или CLI, либо путь, заданный в профиле), никогда не argv и не секрет; CommandSource
// говорит, чья это команда: профиля, стадии или client.command.
type assistantProfileView struct {
	ID                string `json:"id"`
	Title             string `json:"title"`
	Command           string `json:"command"`
	CommandName       string `json:"command_name,omitempty"`
	CommandSource     string `json:"command_source"`
	RuntimeContext    string `json:"runtime_context"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// assistantProfilesResponse — форма ответа GET /api/assistant-profiles.
type assistantProfilesResponse struct {
	Profiles       []assistantProfileView `json:"profiles"`
	MaxDepth       int                    `json:"max_depth"`
	MaxDepthSource string                 `json:"max_depth_source"`
}

// handleAssistantProfiles отдаёт профили помощников с исполнителем, который каждый получит у стадии из
// ?stage_id= (без параметра — у агента рана), и эффективную глубину делегирования. Только чтение: клиент
// передаёт лишь profile_id при создании задачи — здесь нет кода, создающего профили из HTTP-запроса.
func (s *Server) handleAssistantProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	views, err := s.assistantProfiles.Profiles(r.URL.Query().Get("stage_id"))
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	resp := assistantProfilesResponse{Profiles: make([]assistantProfileView, 0, len(views))}
	for _, v := range views {
		resp.Profiles = append(resp.Profiles, assistantProfileView{
			ID:                string(v.ID),
			Title:             v.Title,
			Command:           v.Command,
			CommandName:       s.commandName(v.Command),
			CommandSource:     string(v.CommandSource),
			RuntimeContext:    string(v.RuntimeContext),
			Available:         v.Available,
			UnavailableReason: v.UnavailableReason,
		})
	}
	resp.MaxDepth, resp.MaxDepthSource = s.assistantProfiles.MaxDepth()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
