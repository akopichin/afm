package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/contracts/state"
)

func seedHistory(h *harness, n int) {
	base := time.Unix(1_700_000_000, 0).UTC()
	for i := 1; i <= n; i++ {
		stage := "a"
		if i%2 == 0 {
			stage = "b"
		}
		h.run.hist = append(h.run.hist, state.Transition{
			Seq: uint64(i), Time: base.Add(time.Duration(i) * time.Second), StageID: stage,
			From: state.StatusPending, To: state.StatusRunning, Event: "start", Reason: "r",
		})
	}
}

func TestEventsPageBySeq(t *testing.T) {
	h := newHarness(t, "")
	seedHistory(h, 5)

	first, err := h.svc.readEvents(eventsReadInput{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 2 || first.Events[0].Seq != 1 || first.NextSeq != 2 || !first.HasMore {
		t.Fatalf("first = %+v", first)
	}
	second, _ := h.svc.readEvents(eventsReadInput{AfterSeq: first.NextSeq, Limit: 10})
	if len(second.Events) != 3 || second.Events[0].Seq != 3 || second.NextSeq != 5 || second.HasMore {
		t.Fatalf("second = %+v", second)
	}
	empty, _ := h.svc.readEvents(eventsReadInput{AfterSeq: 5})
	if len(empty.Events) != 0 || empty.NextSeq != 5 || empty.Events == nil {
		t.Fatalf("empty page must keep the cursor and be a list: %+v", empty)
	}
}

func TestEventsLimitDefaultsAndCaps(t *testing.T) {
	h := newHarness(t, "")
	seedHistory(h, maxEventLimit+50)
	page, _ := h.svc.readEvents(eventsReadInput{})
	if len(page.Events) != defaultEventLimit {
		t.Fatalf("default limit gave %d", len(page.Events))
	}
	page, _ = h.svc.readEvents(eventsReadInput{Limit: 100000})
	if len(page.Events) != maxEventLimit || !page.HasMore {
		t.Fatalf("capped limit gave %d, more=%v", len(page.Events), page.HasMore)
	}
}

func TestEventsStageFilterAndValidation(t *testing.T) {
	h := newHarness(t, "")
	seedHistory(h, 6)
	page, _ := h.svc.readEvents(eventsReadInput{StageID: "b"})
	if len(page.Events) != 3 {
		t.Fatalf("stage filter gave %+v", page.Events)
	}
	for _, e := range page.Events {
		if e.StageID != "b" {
			t.Fatalf("foreign event %+v", e)
		}
	}
	if _, err := h.svc.readEvents(eventsReadInput{StageID: "../x"}); err == nil {
		t.Fatal("an invalid stage id must be rejected")
	}
}

func TestEventsRedactReason(t *testing.T) {
	h := newHarness(t, "", func(c *Config) {
		c.RedactSecrets = func(s string) string { return strings.ReplaceAll(s, "SECRET", "[REDACTED]") }
	})
	h.run.hist = []state.Transition{{Seq: 1, StageID: "a", Event: "fail", Reason: "boom SECRET"}}
	page, _ := h.svc.readEvents(eventsReadInput{})
	if page.Events[0].Reason != "boom [REDACTED]" {
		t.Fatalf("reason = %q", page.Events[0].Reason)
	}
}

func TestEventsToolOverMCP(t *testing.T) {
	h := newHarness(t, "")
	seedHistory(h, 3)
	session := h.connect(t, nil)
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name: "afm_events_read", Arguments: map[string]any{"after_seq": 1, "limit": 1},
	})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	var page eventsPage
	decode(t, res, &page)
	if len(page.Events) != 1 || page.Events[0].Seq != 2 || page.NextSeq != 2 || !page.HasMore {
		t.Fatalf("page = %+v", page)
	}
}
