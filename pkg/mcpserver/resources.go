package mcpserver

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/contracts/state"
)

const jsonMIME = "application/json"

// registerResources exposes the same data as the read tools for clients that prefer resources and subscriptions.
func (s *Service) registerResources() {
	s.server.AddResource(&sdkmcp.Resource{
		URI: statusURI(s.runID), Name: "run status", MIMEType: jsonMIME,
		Description: "Run status, pending actions and cursor; subscribe to be told when it changes.",
	}, s.readStatusResource)
	s.server.AddResource(&sdkmcp.Resource{
		URI: costURI(s.runID), Name: "run cost", MIMEType: jsonMIME,
		Description: "Token and cost totals of the run; subscribe to be told when they change.",
	}, s.readCostResource)
	s.server.AddResourceTemplate(&sdkmcp.ResourceTemplate{
		URITemplate: "afm://runs/{run_id}/stages/{stage_id}/plans/{revision}", Name: "stage plan",
		MIMEType:    "text/markdown",
		Description: "The plan of a stage at the revision a client reviewed; a revision that is no longer current is not found.",
	}, s.readPlanResource)
	s.server.AddResourceTemplate(&sdkmcp.ResourceTemplate{
		URITemplate: "afm://runs/{run_id}/artifacts/{artifact_id}", Name: "artifact",
		Description: "An artifact listed by afm_run_status: text (first page) or an image.",
	}, s.readArtifactResource)
}

func (s *Service) readStatusResource(ctx context.Context, req *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
	release, err := s.enter()
	if err != nil {
		return nil, err
	}
	defer release()
	return jsonResource(req.Params.URI, s.buildStatus(ctx))
}

func (s *Service) readCostResource(_ context.Context, req *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
	release, err := s.enter()
	if err != nil {
		return nil, err
	}
	defer release()
	return jsonResource(req.Params.URI, s.costNow())
}

func jsonResource(uri string, v any) (*sdkmcp.ReadResourceResult, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{{URI: uri, MIMEType: jsonMIME, Text: string(raw)}}}, nil
}

// resourceParts splits uri after "afm://runs/<run_id>/" and rejects a URI of another run.
func (s *Service) resourceParts(uri string) ([]string, bool) {
	rest, ok := strings.CutPrefix(uri, "afm://runs/")
	if !ok {
		return nil, false
	}
	parts := strings.Split(rest, "/")
	for i, p := range parts {
		unescaped, err := url.PathUnescape(p)
		if err != nil {
			return nil, false
		}
		parts[i] = unescaped
	}
	if len(parts) < 2 || parts[0] != s.runID {
		return nil, false
	}
	return parts[1:], true
}

func (s *Service) readPlanResource(ctx context.Context, req *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
	release, err := s.enter()
	if err != nil {
		return nil, err
	}
	defer release()
	uri := req.Params.URI
	parts, ok := s.resourceParts(uri)
	if !ok || len(parts) != 4 || parts[0] != "stages" || parts[2] != "plans" || !validStage(parts[1]) {
		return nil, sdkmcp.ResourceNotFoundError(uri)
	}
	stageID, revision := parts[1], parts[3]
	if s.cfg.Approval == nil || s.stageStatus(stageID) != state.StatusAwaitingApproval {
		return nil, sdkmcp.ResourceNotFoundError(uri)
	}
	plan, current, err := s.reviewedPlan(ctx, stageID)
	if err != nil || current != revision {
		return nil, sdkmcp.ResourceNotFoundError(uri)
	}
	text := s.redact(string(plan))
	return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{redactedMeta(
		&sdkmcp.ResourceContents{URI: uri, MIMEType: "text/markdown", Text: text}, text != string(plan))}}, nil
}

// redactedMeta marks content whose secrets were masked, so a client knows its revision names the original bytes.
func redactedMeta(c *sdkmcp.ResourceContents, redacted bool) *sdkmcp.ResourceContents {
	if redacted {
		c.Meta = sdkmcp.Meta{"redacted": true}
	}
	return c
}

func (s *Service) readArtifactResource(ctx context.Context, req *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
	release, err := s.enter()
	if err != nil {
		return nil, err
	}
	defer release()
	uri := req.Params.URI
	parts, ok := s.resourceParts(uri)
	if !ok || len(parts) != 2 || parts[0] != "artifacts" {
		return nil, sdkmcp.ResourceNotFoundError(uri)
	}
	content, err := s.readArtifactLimit(ctx, parts[1], 0, maxTextPage, maxImageFile)
	if err != nil {
		return nil, sdkmcp.ResourceNotFoundError(uri)
	}
	if content.ImageType == "" {
		return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{redactedMeta(
			&sdkmcp.ResourceContents{URI: uri, MIMEType: "text/plain", Text: content.Text}, content.Redacted)}}, nil
	}
	if content.Image == nil {
		return nil, sdkmcp.ResourceNotFoundError(uri)
	}
	return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{{URI: uri, MIMEType: content.ImageType, Blob: content.Image}}}, nil
}
