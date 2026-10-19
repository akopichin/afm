// Package orchestrator (this file): "Interrupt agent" on a stage discussion post — the human asks the running
// stage agent a question it should answer now, not at its next inbox read.
//
// The request is durable with the post itself (DiscussionMessagePayload.Interrupt, same operation). Which
// questions are still open is DERIVED from the journal: an interrupting human post the stage agent has not
// replied to yet. Every prompt of the stage's execution agent (fresh start, restart after the interrupt,
// recovery after a crash, retry) carries those questions, so nothing has to be reconciled at startup and a
// crash at any point — before the interrupt, after the inbox delivery — cannot lose one. The interrupt itself
// is only a way to get there sooner: it restarts a live agent through Revise's running path.
package orchestrator

import (
	"fmt"
	"log"
	"strings"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

// interruptRequested reports whether req asks to interrupt the stage agent: only a human can.
func interruptRequested(req PostMessageRequest) bool {
	return req.Interrupt && req.AuthorKind == assistants.AuthorHuman
}

// interruptForDiscussionQuestion restarts the stage's live agent so its next prompt carries the question
// (openDiscussionQuestions). It acts only when an agent process can actually be interrupted: the stage is
// running and its runner registered an interrupt channel. A stage still in its before-hook or waiting for a
// command slot gets the question in its first prompt instead; any other status reads it at its next turn.
//
// The review-pause gate and the transition are one decision under flowPauseMu, and the transition itself is
// conditional on the stage STILL running (reviseRunning's ExpectFrom), so a concurrent Pause or review pause
// always wins. Nothing is written besides the EvRevise transition, so there is no partial delivery to report.
func (o *Orchestrator) interruptForDiscussionQuestion(stageID, messageID string) {
	o.flowPauseMu.Lock()
	defer o.flowPauseMu.Unlock()
	if o.rejectIfReviewPaused() != nil {
		return
	}
	if _, live := o.interruptChans.Load(stageID); !live {
		return
	}
	noFeedback := func(string) error { return nil }
	if _, _, err := o.reviseRunning(stageID, "discussion question "+messageID, noFeedback); err != nil {
		log.Printf("WARN: discussion %s: interrupt stage %s: %v", messageID, stageID, err)
	}
}

// openDiscussionQuestions returns the interrupting human posts of stageID the stage agent has not replied to.
func openDiscussionQuestions(rs state.RunState, stageID string) []prompts.DiscussionQuestion {
	stageAgent := string(meshStageParticipantID(stageID))
	answered := map[string]bool{}
	for _, id := range rs.Assistants.Discussions.ByStage[stageID] {
		if m := rs.Assistants.Discussions.Messages[id]; m != nil && m.ParentMessageID != "" && m.Author.ID == stageAgent {
			answered[m.ParentMessageID] = true
		}
	}
	var open []prompts.DiscussionQuestion
	for _, id := range rs.Assistants.Discussions.ByStage[stageID] {
		m := rs.Assistants.Discussions.Messages[id]
		if m != nil && m.Interrupt && m.Author.Kind == assistants.AuthorHuman && !answered[id] {
			open = append(open, prompts.DiscussionQuestion{MessageID: id, Body: m.Body})
		}
	}
	return open
}

// discussionQuestionNote is the prompt block of stageID's open discussion questions, "" when there are none.
func (o *Orchestrator) discussionQuestionNote(stageID string) string {
	if o.opts.Store == nil {
		return ""
	}
	return prompts.DiscussionQuestionBlock(openDiscussionQuestions(o.opts.Store.Snapshot(), stageID))
}

// unansweredHumanPosts returns the human posts of stageID that nobody replied to, once the stage has finished
// (done/failed) — while it runs, a post is simply not answered YET. The prompt asks agents to answer the
// human, but a stage can still finish without doing so (e.g. after its extra turns for unread messages ran
// out); this is how the Discussion says so instead of implying an answer is coming.
func unansweredHumanPosts(rs state.RunState, stageID string) []string {
	st, ok := rs.Stages[stageID]
	if !ok || (st.Status != state.StatusDone && st.Status != state.StatusFailed) {
		return nil
	}
	replied := map[string]bool{}
	for _, id := range rs.Assistants.Discussions.ByStage[stageID] {
		if m := rs.Assistants.Discussions.Messages[id]; m != nil && m.ParentMessageID != "" && m.Author.Kind != assistants.AuthorHuman {
			replied[m.ParentMessageID] = true
		}
	}
	var out []string
	for _, id := range rs.Assistants.Discussions.ByStage[stageID] {
		if m := rs.Assistants.Discussions.Messages[id]; m != nil && m.Author.Kind == assistants.AuthorHuman && !replied[id] {
			out = append(out, id)
		}
	}
	return out
}

// UnansweredDiscussionPosts is unansweredHumanPosts for the dashboard: the set of stageID's human posts that
// finished without a reply.
func (o *Orchestrator) UnansweredDiscussionPosts(stageID string) map[string]bool {
	ids := unansweredHumanPosts(o.opts.Store.Snapshot(), stageID)
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// noticeUnansweredHumanPosts publishes a durable feed notice when stageID finishes with human discussion posts
// nobody answered. Called from the terminal-transition hook.
func (o *Orchestrator) noticeUnansweredHumanPosts(stageID string) {
	if !o.opts.AssistantsEnabled {
		return
	}
	ids := unansweredHumanPosts(o.opts.Store.Snapshot(), stageID)
	if len(ids) == 0 {
		return
	}
	o.publishNotice(bus.Event{Type: bus.EventContextWarning, StageID: stageID, Data: fmt.Sprintf(
		"stage finished without answering %d of your discussion message(s): %s", len(ids), strings.Join(ids, ", "))})
}
