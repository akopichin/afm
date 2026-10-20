package afmsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Approve approves a stage plan currently awaiting approval
// (POST /api/stages/<id>/approve).
func (r *Run) Approve(ctx context.Context, stageID string) error {
	return r.post(ctx, "/api/stages/"+url.PathEscape(stageID)+"/approve", nil)
}

// Retry retries a failed stage (POST /api/stages/<id>/retry).
func (r *Run) Retry(ctx context.Context, stageID string) error {
	return r.post(ctx, "/api/stages/"+url.PathEscape(stageID)+"/retry", nil)
}

// Revise sends feedback for plan revision (POST /api/stages/<id>/revise).
func (r *Run) Revise(ctx context.Context, stageID, feedback string) error {
	body, err := json.Marshal(map[string]string{"feedback": feedback})
	if err != nil {
		return fmt.Errorf("afmsdk: revise: encode request: %w", err)
	}
	return r.post(ctx, "/api/stages/"+url.PathEscape(stageID)+"/revise", body)
}

func (r *Run) post(ctx context.Context, path string, body []byte) error {
	// Existing controls keep their historical raw JSON request body. The shared
	// request path provides bounded reads and inspectable HTTP failures.
	var payload any
	if body != nil {
		payload = json.RawMessage(body)
	}
	_, err := r.request(ctx, http.MethodPost, path, payload, maxJSONBytes)
	return err
}

// Pause and Continue control an eligible stage. The dashboard does not accept
// an expected stage sequence, so callers must perform their own fresh read.
func (r *Run) Pause(ctx context.Context, stageID string) error {
	return r.post(ctx, stagePath(stageID, "/pause"), nil)
}

func (r *Run) Continue(ctx context.Context, stageID string) error {
	return r.post(ctx, stagePath(stageID, "/continue"), nil)
}
