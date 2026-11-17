package mcpserver

import (
	"context"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultEventLimit = 100
	maxEventLimit     = 500
)

// eventsReadInput is the argument of afm_events_read.
type eventsReadInput struct {
	AfterSeq uint64 `json:"after_seq,omitempty" jsonschema:"return events with a higher seq; 0 starts at the beginning"`
	Limit    int    `json:"limit,omitempty" jsonschema:"page size, default 100, at most 500"`
	StageID  string `json:"stage_id,omitempty" jsonschema:"only this stage's events"`
}

// eventView is one stage transition of the run journal.
type eventView struct {
	Seq     uint64    `json:"seq"`
	Time    time.Time `json:"time"`
	StageID string    `json:"stage_id"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	Event   string    `json:"event"`
	Reason  string    `json:"reason,omitempty"`
}

type eventsPage struct {
	Events []eventView `json:"events"`
	// NextSeq is the after_seq of the next page; it equals the request's after_seq when nothing new arrived.
	NextSeq uint64 `json:"next_seq"`
	HasMore bool   `json:"has_more"`
}

// readEvents pages the journal by seq. Seq is the only cursor here; it says nothing about attention.
func (s *Service) readEvents(in eventsReadInput) (eventsPage, error) {
	if in.StageID != "" && !validStage(in.StageID) {
		return eventsPage{}, &ToolError{Code: codeInvalid, Kind: codeInvalid, Message: "invalid stage_id"}
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultEventLimit
	}
	limit = min(limit, maxEventLimit)
	history, err := s.cfg.Reader.History()
	if err != nil {
		return eventsPage{}, err
	}
	page := eventsPage{Events: []eventView{}, NextSeq: in.AfterSeq}
	for _, t := range history {
		if t.Seq <= in.AfterSeq || (in.StageID != "" && t.StageID != in.StageID) {
			continue
		}
		if len(page.Events) == limit {
			page.HasMore = true
			break
		}
		page.Events = append(page.Events, eventView{
			Seq: t.Seq, Time: t.Time, StageID: t.StageID, From: string(t.From), To: string(t.To),
			Event: t.Event, Reason: s.redact(t.Reason),
		})
		page.NextSeq = t.Seq
	}
	return page, nil
}

func (s *Service) registerEventsTool() {
	addTool(s, toolEventsRead, func(_ context.Context, _ *sdkmcp.CallToolRequest, in eventsReadInput) (*sdkmcp.CallToolResult, any, error) {
		release, err := s.enter()
		if err != nil {
			return errorResult(err), nil, nil
		}
		defer release()
		page, err := s.readEvents(in)
		if err != nil {
			return errorResult(err), nil, nil
		}
		return okResult(page), nil, nil
	})
}
