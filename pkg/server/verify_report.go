package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

// maxVerifyReportBytes caps the report.md we'll read into memory. report.md
// is never truncated on write (stagefiles.WriteReport), but a runaway model
// output still shouldn't be streamed unbounded into the dashboard.
const maxVerifyReportBytes = 1 << 20 // 1 MiB

// handleVerifyReport serves the human-readable report.md of one AI-verify
// pass (V5b.3), addressed OPAQUELY by stage id + verification id — never an
// arbitrary client-supplied filesystem path (the "report_path" field the
// verify_result notice/event carries is for display/identification only;
// the dashboard must never feed it back to the server as a path). Mirrors
// handleArtifact's os.OpenRoot pattern: the root is pinned at runDir, so any
// traversal/symlink escape in either id is rejected by the kernel/os.Root
// itself, not by string matching alone.
func (s *Server) handleVerifyReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, verID, ok := parseVerifyReportPath(r.URL.Path)
	if !ok || !isValidStageID(id) || !isValidStageID(verID) {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	root, err := os.OpenRoot(s.runDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()

	// root.Open resolves the path relative to the pinned runDir fd — any
	// symlink component escaping the root is rejected by os.Root itself
	// (TOCTOU-free, same guarantee as handleArtifact).
	f, err := root.Open(filepath.Join(id, stagefiles.ReportRelPath(verID)))
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
	if fi.Size() > maxVerifyReportBytes {
		http.Error(w, "report too large", http.StatusRequestEntityTooLarge)
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
	// report.md at this path is NOT guaranteed immutable: a multi-step verify
	// pass (several agent steps, all passing) rewrites report.md at the same
	// verID mid-pass, once per step. This is just a short bounded cache of
	// whatever was last written — a client can briefly see a stale (earlier
	// step's) report during an in-progress pass — kept private (never a
	// shared/CDN cache). See "storing report.md per-step instead of
	// per-pass" in the AI-verify plan for the real fix (deferred follow-up).
	w.Header().Set("Cache-Control", "private, max-age=60")
	_ = json.NewEncoder(w).Encode(map[string]string{"content": string(data)})
}

// parseVerifyReportPath splits /api/stages/{id}/verify/{verID}/report into
// id and verID. It returns ok=false when the path does not carry both
// segments in the expected shape.
func parseVerifyReportPath(path string) (id, verID string, ok bool) {
	rest := strings.TrimPrefix(path, "/api/stages/")
	rest = strings.TrimSuffix(rest, "/report")
	parts := strings.SplitN(rest, "/verify/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
