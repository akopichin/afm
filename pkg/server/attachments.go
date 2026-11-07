package server

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/akopichin/afm-engine/engine"
)

// allowedAttachmentTypes maps an accepted Content-Type to the file extension used when persisting the
// upload.
var allowedAttachmentTypes = map[string]string{
	mimePNG:  ".png",
	mimeJPEG: ".jpg",
	mimeWebP: ".webp",
	mimeGIF:  ".gif",
}

// handleUploadAttachment saves a pasted clipboard image under the stage's attachments directory and returns
// its absolute path as {"path": "..."}.
func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	stageID, ok := stageCommandID(w, r, "/attachments")
	if !ok {
		return
	}

	ext, ok := allowedAttachmentTypes[r.Header.Get("Content-Type")]
	if !ok {
		http.Error(w, "unsupported image type", http.StatusUnsupportedMediaType)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, engine.MaxAttachmentBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "attachment too large", http.StatusRequestEntityTooLarge)
		return
	}

	path, err := s.actions.SaveAttachment(stageID, ext, data)
	if err != nil {
		writeCommandError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"path": path})
}
