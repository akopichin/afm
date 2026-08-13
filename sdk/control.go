package afmsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	u, err := url.Parse(r.baseURL)
	if err != nil {
		return fmt.Errorf("afmsdk: %s: %w", path, err)
	}
	u.Path = path
	u.RawPath = path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), reader)
	if err != nil {
		return fmt.Errorf("afmsdk: %s: %w", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("afmsdk: %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("afmsdk: %s: unexpected response %s: %s", path, resp.Status, bytes.TrimSpace(respBody))
	}
	return nil
}
