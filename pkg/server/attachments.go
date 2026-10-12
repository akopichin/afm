package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// maxAttachmentBytes caps a single pasted-image upload.
const maxAttachmentBytes = 10 << 20

// allowedAttachmentTypes maps an accepted Content-Type to the file extension used when persisting the
// upload.
var allowedAttachmentTypes = map[string]string{
	mimePNG:  ".png",
	mimeJPEG: ".jpg",
	mimeWebP: ".webp",
	mimeGIF:  ".gif",
}

// handleUploadAttachment saves a pasted clipboard image to <runDir>/<stageID>/attachments/paste-<id>.<ext>
// and returns its absolute path as {"path": "..."}.
func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/attachments")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}

	ext, ok := allowedAttachmentTypes[r.Header.Get("Content-Type")]
	if !ok {
		http.Error(w, "unsupported image type", http.StatusUnsupportedMediaType)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "attachment too large", http.StatusRequestEntityTooLarge)
		return
	}
	if len(data) == 0 {
		http.Error(w, "empty attachment", http.StatusBadRequest)
		return
	}

	attachmentsDir := filepath.Join(s.runDir, stageID, "attachments")
	if err := os.MkdirAll(attachmentsDir, 0755); err != nil {
		http.Error(w, "mkdir attachments: "+err.Error(), http.StatusInternalServerError)
		return
	}

	name := "paste-" + randomAttachmentID() + ext
	path := filepath.Join(attachmentsDir, name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		http.Error(w, "write attachment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"path": absPath})
}

// randomAttachmentID mirrors newRunID's random-suffix shape: 8 random bytes as hex, generated fresh per
// upload so concurrent pastes in the same stage never collide on a filename.
func randomAttachmentID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
