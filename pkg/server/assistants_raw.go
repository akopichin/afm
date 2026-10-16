package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"

	"github.com/akopichin/afm/pkg/orchestrator"
)

// maxAssistantRawBytes caps the raw answer file we'll read into memory — same discipline as
// maxVerifyReportBytes.
const maxAssistantRawBytes = 1 << 20 // 1 MiB

// handleAssistantRawResult serves the raw text a helper task's final turn produced — addressed OPAQUELY by
// task id, under os.OpenRoot(s.runDir) so no symlink or ".." component can escape the run directory. Mirrors
// handleVerifyReport's shape, with one deliberate deviation: Cache-Control is "no-store", not a positive
// max-age — result.json.raw is rewritten (or cleared) on every turn of the SAME task id, so any cached copy
// could show an answer the file no longer holds.
func (s *Server) handleAssistantRawResult(w http.ResponseWriter, r *http.Request, taskID string) {
	if s.assistantsReader == nil {
		http.NotFound(w, r)
		return
	}

	root, err := os.OpenRoot(s.runDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()

	f, err := root.Open(orchestrator.TaskRawResultRelPath(taskID))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if fi.Size() > maxAssistantRawBytes {
		http.Error(w, "raw answer too large", http.StatusRequestEntityTooLarge)
		return
	}

	data, err := io.ReadAll(f)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"content": string(data)})
}
