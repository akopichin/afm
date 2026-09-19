package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	// Blank imports register the decoders whose formats we accept. The
	// allowlist below is enforced against the format string image.DecodeConfig
	// returns, so an unregistered format (e.g. SVG, which stdlib has no decoder
	// for at all) can never be served.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// maxArtifactBytes mirrors maxAttachmentBytes: an agent-produced image artifact
// larger than 10 MiB is refused rather than streamed.
const maxArtifactBytes = 10 << 20

// maxArtifactDim caps the pixel width/height read from the image header, a cheap
// guard against decompression-bomb artifacts (image.DecodeConfig reads only the
// header, so this is checked before any pixel allocation would happen client-side).
const maxArtifactDim = 8192

// artifactContentTypes maps a confirmed decoder format to the Content-Type we
// serve. The value comes from image.DecodeConfig, never from the file
// extension, so a mislabeled ".png" that is really something else is rejected.
var artifactContentTypes = map[string]string{
	"png":  "image/png",
	"jpeg": "image/jpeg",
	"gif":  "image/gif",
}

// handleArtifact serves an immutable image artifact a stage published under
// <runDir>/<stageID>/artifacts/<name>. Addressing is opaque (stageID + name);
// no client-supplied path is ever joined. Every "not found / not allowed"
// outcome collapses to a single 404 so no filesystem detail or raw error leaks.
func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, name, ok := parseArtifactPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !isValidStageID(id) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	if !isValidArtifactName(name) {
		http.NotFound(w, r)
		return
	}

	// Pin the root at runDir (afm-owned, created before any agent runs), NOT at
	// the artifacts directory: os.OpenRoot FOLLOWS symlinks in the directory NAME
	// it opens, so pinning at `<runDir>/<id>/artifacts` would follow an agent-
	// planted `artifacts` symlink out of the run dir. Opening the whole relative
	// path `<id>/artifacts/<name>` THROUGH a root pinned at runDir makes os.Root
	// resolve every component (incl. `<id>` and `artifacts`) and reject any that
	// escapes runDir — a symlink `artifacts -> /etc` (or `../../…`) is refused.
	// id and name are already validated (no `..`, safe charset).
	root, err := os.OpenRoot(s.runDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()

	// root.Open resolves the path relative to the pinned runDir fd. Any symlink
	// component escaping the root is rejected by os.Root itself — there is no
	// EvalSymlinks-then-check-prefix window (TOCTOU-free).
	f, err := root.Open(filepath.Join(id, "artifacts", name))
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
	if fi.Size() > maxArtifactBytes {
		http.Error(w, "artifact too large", http.StatusRequestEntityTooLarge)
		return
	}

	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		http.Error(w, "unsupported artifact type", http.StatusUnsupportedMediaType)
		return
	}
	contentType, ok := artifactContentTypes[format]
	if !ok {
		http.Error(w, "unsupported artifact type", http.StatusUnsupportedMediaType)
		return
	}
	if cfg.Width > maxArtifactDim || cfg.Height > maxArtifactDim {
		http.Error(w, "artifact dimensions too large", http.StatusRequestEntityTooLarge)
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// NOT immutable: the same stage dir is reused across retry/revision and the
	// prompt permits renaming over the same final name (e.g. chart.png), so the
	// bytes behind a URL CAN change within a run. `no-cache` lets the browser
	// store the response but forces revalidation against the ETag on every use —
	// a 304 (cheap) when unchanged, fresh bytes when the file was overwritten.
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", artifactETag(name, fi.Size(), fi.ModTime().UnixNano()))

	// ServeContent honors If-None-Match/If-Modified-Since (→ 304) and Range,
	// and will not override the Content-Type we already set.
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// parseArtifactPath splits /api/stages/{id}/artifacts/{name} into id and name.
// It returns ok=false when the path does not carry both segments.
func parseArtifactPath(path string) (id, name string, ok bool) {
	rest := strings.TrimPrefix(path, "/api/stages/")
	parts := strings.SplitN(rest, "/artifacts/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// artifactETag is a quoted, opaque validator derived from name, size and mtime.
func artifactETag(name string, size, mtimeNano int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", name, size, mtimeNano)))
	return `"` + hex.EncodeToString(sum[:]) + `"`
}
