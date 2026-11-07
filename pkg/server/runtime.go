package server

import "net/http"

const (
	runtimeProtocolVersion = 1
	runtimeModeDashboard   = "dashboard"
)

// RuntimeInfo описывает режим процесса для /api/runtime; нулевое значение читается как «dashboard, не готов».
type RuntimeInfo struct {
	Mode  string
	Ready func() bool
}

type runtimeResponse struct {
	ProtocolVersion int    `json:"protocol_version"`
	Mode            string `json:"mode"`
	RunID           string `json:"run_id"`
	Ready           bool   `json:"ready"`
}

// handleRuntime отдаёт клиентам (SDK) режим, готовность и id рана без путей, токенов и PID.
func (s *Server) handleRuntime(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp := runtimeResponse{
		ProtocolVersion: runtimeProtocolVersion,
		Mode:            s.runtime.Mode,
		RunID:           s.runID(),
		Ready:           s.runtime.Ready != nil && s.runtime.Ready(),
	}
	if resp.Mode == "" {
		resp.Mode = runtimeModeDashboard
	}
	writeJSON(w, resp)
}
