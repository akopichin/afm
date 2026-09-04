package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/akopichin/afm/pkg/server/workspace"
)

// routeFiles dispatches GET /api/files/<action> to the matching read-only
// handler. The whole capability is off (404, indistinguishable from an
// unknown route) when no workspace.FS is configured or it has zero roots —
// this is how host-mode afm (no Docker project mount) hides the endpoints
// entirely instead of exposing an empty/erroring API surface.
func (s *Server) routeFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || s.workspace == nil || len(s.workspace.Roots()) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch strings.TrimPrefix(r.URL.Path, "/api/files/") {
	case "roots":
		s.filesRoots(w, r)
	case "tree":
		s.filesTree(w, r)
	case "reference":
		s.filesReference(w, r)
	case "content":
		s.filesContent(w, r)
	case "diff":
		s.filesDiff(w, r)
	case "changed":
		s.filesChanged(w, r)
	case "search":
		s.filesSearch(w, r)
	default:
		http.NotFound(w, r)
	}
}

// writeFilesError writes the scoped JSON error shape used only by the
// /api/files/* handlers — {"error": "<code>"}. It never includes the
// underlying error's text, so an absolute filesystem path from a
// workspace.FS error can't leak into the response body.
func writeFilesError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// filesErrStatus maps a workspace.FS sentinel error to an HTTP status and a
// stable machine-readable error code. workspace.ErrReadFailed and any other
// unrecognized error fall into the default 500 read_failed case.
func filesErrStatus(err error) (int, string) {
	switch {
	case errors.Is(err, workspace.ErrInvalidRootOrPath):
		return http.StatusBadRequest, "invalid_root_or_path"
	case errors.Is(err, workspace.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, workspace.ErrDiffUnavailable):
		return http.StatusConflict, "diff_unavailable"
	case errors.Is(err, workspace.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, "file_too_large"
	case errors.Is(err, workspace.ErrBinary):
		return http.StatusUnsupportedMediaType, "binary_file"
	case errors.Is(err, workspace.ErrSymlink):
		return http.StatusUnprocessableEntity, "symlink_not_supported"
	default:
		return http.StatusInternalServerError, "read_failed"
	}
}

func (s *Server) filesRoots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string][]workspace.RootView{"roots": s.workspace.Roots()})
}

func (s *Server) filesTree(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, err := s.workspace.List(r.Context(), q.Get("root"), q.Get("path"), q.Get("cursor"))
	if err != nil {
		status, code := filesErrStatus(err)
		writeFilesError(w, status, code)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(page)
}

func (s *Server) filesReference(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ref, err := s.workspace.Reference(r.Context(), q.Get("root"), q.Get("path"))
	if err != nil {
		status, code := filesErrStatus(err)
		writeFilesError(w, status, code)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(ref)
}

func (s *Server) filesContent(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, err := s.workspace.Read(r.Context(), q.Get("root"), q.Get("path"))
	if err != nil {
		status, code := filesErrStatus(err)
		writeFilesError(w, status, code)
		return
	}
	if f.ETag != "" && r.Header.Get("If-None-Match") == f.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if f.ETag != "" {
		w.Header().Set("ETag", f.ETag)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(f)
}

func (s *Server) filesDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d, err := s.workspace.Diff(r.Context(), q.Get("root"), q.Get("path"))
	if err != nil {
		status, code := filesErrStatus(err)
		writeFilesError(w, status, code)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(d)
}

func (s *Server) filesChanged(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := s.workspace.Changes(r.Context(), q.Get("root"), workspace.ChangeMode(q.Get("mode")))
	if err != nil {
		status, code := filesErrStatus(err)
		writeFilesError(w, status, code)
		return
	}
	// Dynamic listing: Refresh must never get a browser-cached body.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(list)
}

func (s *Server) filesSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res, err := s.workspace.Search(r.Context(), q.Get("root"), q.Get("q"))
	if err != nil {
		status, code := filesErrStatus(err)
		writeFilesError(w, status, code)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(res)
}
