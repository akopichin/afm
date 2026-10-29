package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

const errCodeInvalidCursor = "invalid_cursor"

// handleAssistantActivity serves GET /api/assistants/{id}/activity: a bounded page of the helper's saved
// activity. The client names only the task, a cursor and a limit; the run's notices file is chosen server-side.
func (s *Server) handleAssistantActivity(w http.ResponseWriter, r *http.Request, taskID string) {
	if s.assistantsReader == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	query, errCode := parseActivityQuery(r.URL.Query())
	if errCode != "" {
		writeAssistantErrCode(w, http.StatusBadRequest, errCode)
		return
	}
	page, err := s.assistantsReader.TaskActivity(taskID, query)
	if errors.Is(err, stagefiles.ErrInvalidCursor) {
		writeAssistantErrCode(w, http.StatusBadRequest, errCodeInvalidCursor)
		return
	}
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

// parseActivityQuery validates limit (default 100, capped at 200), after and before; at most one cursor.
// It returns an error code for a malformed value instead of guessing what the client meant.
func parseActivityQuery(v url.Values) (stagefiles.ActivityQuery, string) {
	q := stagefiles.ActivityQuery{Limit: stagefiles.DefaultActivityLimit}
	limit, ok := optionalUint(v, "limit")
	if !ok || (limit != nil && *limit == 0) {
		return q, "invalid_limit"
	}
	if limit != nil {
		q.Limit = int(min(*limit, stagefiles.MaxActivityLimit))
	}
	var afterOK, beforeOK bool
	q.After, afterOK = optionalUint(v, "after")
	q.Before, beforeOK = optionalUint(v, "before")
	if !afterOK || !beforeOK || (q.After != nil && q.Before != nil) {
		return q, errCodeInvalidCursor
	}
	return q, ""
}

// optionalUint reads a query parameter that is either absent (nil) or given exactly once as a decimal unsigned
// integer; anything else is not ok.
func optionalUint(v url.Values, key string) (n *uint64, ok bool) {
	if !v.Has(key) {
		return nil, true
	}
	if len(v[key]) != 1 {
		return nil, false
	}
	parsed, err := strconv.ParseUint(v.Get(key), 10, 64)
	if err != nil {
		return nil, false
	}
	return &parsed, true
}
