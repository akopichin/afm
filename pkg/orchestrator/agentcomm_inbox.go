package orchestrator

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/memory"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 4, T4.4: the <inbox> block and its delivery marking. A turn's prompt and the set of
// messages marked delivered for it come from ONE read of the state (agentCommInboxFor): a message that
// arrives after the prompt was built is not in it, so it must stay unseen and reach the next turn.

// inboxBlockByteBudget bounds how much message text one turn's <inbox> block carries; inboxMessageOverhead
// approximates each message's header line. Messages past the budget stay accepted for the next turn (or an
// `afm agent inbox` call), and the block says how many are still waiting.
const (
	inboxBlockByteBudget = 64 * 1024
	inboxMessageOverhead = 256
)

// agentCommInboxFor renders participant's pending (not-yet-delivered) inbox messages within inboxBlockByteBudget
// (see renderInbox for the order), and returns a closure that marks exactly those messages delivered. The same
// set is remembered as the participant's rendered inbox for deliverRenderedInbox — executor.Config.OnDelivered,
// called only after cmd.Start() succeeds (pkg/executor/executor.go). Marking never happens at
// prompt-assembly time, so a crash or a failed start between building this prompt and spawning the process
// leaves the messages at "accepted" and they are re-injected, unchanged, on the next attempt.
func (o *Orchestrator) agentCommInboxFor(participant agentcomm.ParticipantID) (block string, markDelivered func()) {
	block, _, markDelivered = o.renderInbox(participant, nil)
	return block, markDelivered
}

// renderInbox is agentCommInboxFor that also re-renders unfinished: the messages an earlier turn of the same
// agent was handed but never finished (see turnInboxFileName). They are already delivered, yet that turn's
// input would otherwise be lost to its retry. The budget goes to three groups in turn — the unfinished input,
// then waking messages, then passive turn results (Message.AutoResult) — each oldest first, so results that
// arrived while the agent was idle never push out the message that starts its turn. It returns the ids it
// rendered, in that order.
func (o *Orchestrator) renderInbox(participant agentcomm.ParticipantID, unfinished []agentcomm.MessageID) (block string, rendered []agentcomm.MessageID, markDelivered func()) {
	if o.opts.Store == nil {
		// Some tests build a bare Orchestrator literal to exercise a single concern (e.g. browser/mesh Config
		// wiring) without a real Store — nothing to read, so there is nothing pending and nothing to deliver.
		return "", nil, func() {}
	}
	rs := o.opts.Store.Snapshot()
	mesh := rs.Assistants.Mesh

	var again, waking, passive []*agentcomm.Message
	for _, id := range mesh.Inbox[participant] {
		m := mesh.Messages[id]
		switch {
		case m == nil:
		case slices.Contains(unfinished, id):
			again = append(again, m)
		case mesh.Receipts[id][participant] != agentcomm.DeliveryAccepted:
		case m.AutoResult:
			passive = append(passive, m)
		default:
			waking = append(waking, m)
		}
	}

	var msgs []prompts.AgentCommInboxMessage
	size, more := 0, 0
	for _, m := range slices.Concat(again, waking, passive) {
		size += len(m.Body) + inboxMessageOverhead
		if len(msgs) > 0 && size > inboxBlockByteBudget {
			more++
			continue
		}
		rendered = append(rendered, m.ID)
		msgs = append(msgs, inboxMessageView(m))
	}

	o.renderedInbox.Store(participant, rendered)
	return prompts.AgentCommInboxBlock(msgs, more), rendered, func() { o.markInboxDelivered(participant, rendered) }
}

// Bounds of the <your_requests> block, which repeats on every turn: at most maxListedRequests requests, the start
// of each request and of each answer, and requestsBlockBudget bytes of them in all (requestEntryOverhead
// approximates one entry's header line).
const (
	maxListedRequests    = 20
	requestExcerptBytes  = 300
	answerExcerptBytes   = 2 << 10
	requestsBlockBudget  = 24 << 10
	requestEntryOverhead = 256
)

// sentRequest is one request participant sent, with what became of the waits for its answer.
type sentRequest struct {
	msg     *agentcomm.Message
	answers []*agentcomm.Message // oldest first
	waiting bool                 // a wait for its answer is still open
	ended   *state.MeshWait      // the latest wait for it that ended without an answer
}

// agentCommRequestsBlock renders participant's <your_requests> block (prompts.AgentCommRequestsBlock): the
// requests it sent — messages of kind request, or ones it registered a reply_to wait on — each with its answers,
// its open wait or the wait that ended without an answer. Requests still waiting come first, then the newest
// others, within maxListedRequests and requestsBlockBudget; an entry keeps its newest answers that fit. The block
// says how many requests and answers it left out.
func agentCommRequestsBlock(mesh state.MeshState, participant agentcomm.ParticipantID) string {
	requests := sentRequests(mesh, participant)
	// Priority: open requests newest first, then the others newest first.
	order := slices.Clone(requests)
	slices.SortStableFunc(order, func(a, b *sentRequest) int {
		if a.waiting != b.waiting {
			if a.waiting {
				return -1
			}
			return 1
		}
		return cmp.Compare(b.msg.Seq, a.msg.Seq)
	})
	budget := requestsBlockBudget
	var picked []prompts.AgentCommRequest
	pickedSeq := map[string]uint64{}
	omittedRequests, omittedAnswers := 0, 0
	for _, r := range order {
		entry := requestView(mesh, r)
		cost := len(entry.Body) + requestEntryOverhead
		if len(picked) >= maxListedRequests || (len(picked) > 0 && cost > budget) {
			omittedRequests++
			continue
		}
		budget -= cost
		var kept []prompts.AgentCommInboxMessage
		for i := len(r.answers) - 1; i >= 0; i-- {
			view := inboxMessageView(r.answers[i])
			view.Body = truncateText(view.Body, answerExcerptBytes)
			if c := len(view.Body) + requestEntryOverhead; c <= budget {
				budget -= c
				kept = append(kept, view)
				continue
			}
			omittedAnswers += i + 1
			break
		}
		slices.Reverse(kept)
		entry.Answers = kept
		picked = append(picked, entry)
		pickedSeq[entry.MessageID] = r.msg.Seq
	}
	slices.SortFunc(picked, func(a, b prompts.AgentCommRequest) int {
		return cmp.Compare(pickedSeq[a.MessageID], pickedSeq[b.MessageID])
	})
	return prompts.AgentCommRequestsBlock(picked, omittedRequests, omittedAnswers)
}

// sentRequests collects participant's requests (see agentCommRequestsBlock), oldest first.
func sentRequests(mesh state.MeshState, participant agentcomm.ParticipantID) []*sentRequest {
	byID := map[agentcomm.MessageID]*sentRequest{}
	waitsOn := map[agentcomm.MessageID][]*state.MeshWait{}
	for _, w := range mesh.Waits {
		if w.Waiter == participant && w.Condition.Kind == agentcomm.WaitOnReplyTo {
			waitsOn[w.Condition.MessageID] = append(waitsOn[w.Condition.MessageID], w)
		}
	}
	var out []*sentRequest
	for _, m := range mesh.Messages {
		if _, waited := waitsOn[m.ID]; m.Sender == participant && (m.Kind == agentcomm.MessageKindRequest || waited) {
			r := &sentRequest{msg: m}
			for _, w := range waitsOn[m.ID] {
				switch w.Status {
				case agentcomm.WaitPending:
					r.waiting = true
				case agentcomm.WaitCancelled, agentcomm.WaitExpired, agentcomm.WaitRunClosed:
					if r.ended == nil || w.RegisteredSeq > r.ended.RegisteredSeq {
						r.ended = w
					}
				default:
				}
			}
			byID[m.ID] = r
			out = append(out, r)
		}
	}
	for _, m := range mesh.Messages {
		if m.InReplyTo == nil || m.Sender == participant {
			continue
		}
		if r := byID[*m.InReplyTo]; r != nil {
			r.answers = append(r.answers, m)
		}
	}
	bySeq := func(a, b *agentcomm.Message) int { return cmp.Compare(a.Seq, b.Seq) }
	for _, r := range out {
		slices.SortFunc(r.answers, bySeq)
	}
	slices.SortFunc(out, func(a, b *sentRequest) int { return bySeq(a.msg, b.msg) })
	return out
}

// requestView is r without its answers, as the block shows it.
func requestView(mesh state.MeshState, r *sentRequest) prompts.AgentCommRequest {
	v := prompts.AgentCommRequest{MessageID: string(r.msg.ID), Body: truncateText(r.msg.Body, requestExcerptBytes), Waiting: r.waiting}
	for _, to := range r.msg.Recipients {
		v.To = append(v.To, participantLabel(mesh, to))
	}
	if r.ended != nil && !r.waiting && len(r.answers) == 0 {
		v.WaitEnded = string(r.ended.Status)
		if r.ended.Reason != "" {
			v.WaitEnded += ": " + r.ended.Reason
		}
	}
	return v
}

// participantLabel names id the way `afm agent send --to` takes it: its alias, or the id when it has none.
func participantLabel(mesh state.MeshState, id agentcomm.ParticipantID) string {
	if p := mesh.Participants[id]; p != nil && p.Alias != "" {
		return p.Alias
	}
	return string(id)
}

func inboxMessageView(m *agentcomm.Message) prompts.AgentCommInboxMessage {
	var inReplyTo string
	if m.InReplyTo != nil {
		inReplyTo = string(*m.InReplyTo)
	}
	recipients := make([]string, len(m.Recipients))
	for i, r := range m.Recipients {
		recipients[i] = string(r)
	}
	return prompts.AgentCommInboxMessage{
		MessageID: string(m.ID), ThreadID: string(m.ThreadID), From: string(m.Sender),
		Recipients: recipients, InReplyTo: inReplyTo, Body: m.Body, AutoResult: m.AutoResult,
	}
}

// deliverRenderedInbox returns the executor.Config.OnDelivered callback for participant: it marks the
// messages of participant's latest rendered <inbox> block delivered — never a fresh read of the inbox, which
// could include a message that arrived after the prompt was built.
func (o *Orchestrator) deliverRenderedInbox(participant agentcomm.ParticipantID) func() {
	return func() {
		if v, ok := o.renderedInbox.LoadAndDelete(participant); ok {
			o.markInboxDelivered(participant, v.([]agentcomm.MessageID))
		}
	}
}

// markInboxDelivered records the handover of ids to participant. A failed journal write is storage-fatal and
// ends the run (handleTaskOperationErr): the messages are already in the agent's prompt, and leaving them
// accepted would hand them over again on another turn. A rejection (the run is finalizing) changes nothing.
func (o *Orchestrator) markInboxDelivered(participant agentcomm.ParticipantID, ids []agentcomm.MessageID) {
	for _, id := range ids {
		if err := o.MarkMessageDelivered(context.Background(), id, participant); err != nil {
			o.handleTaskOperationErr(err)
			return
		}
	}
}

// agentCommInboxForTask is renderInbox's helper-task-scoped wrapper, used by buildTaskContext (T4.3): the
// task's unfinished turn input (if any) is rendered again together with its pending messages.
func (o *Orchestrator) agentCommInboxForTask(taskID assistants.TaskID) (block string, rendered []agentcomm.MessageID, markDelivered func()) {
	return o.renderInbox(o.taskMeshParticipant(taskID), o.unfinishedTurnInput(taskID))
}

// unfinishedTurnInput returns the messages handed to taskID's latest turn while that turn has not finished: the
// executor marks them delivered when the process starts, so if the turn is cut short (run shutdown, a crash,
// Pause) only this record hands them to its retry. The journal is the source (state.TaskState.TurnInbox); a
// journal written before turns were recorded has no turn yet and falls back to the turn-inbox.json it left.
func (o *Orchestrator) unfinishedTurnInput(taskID assistants.TaskID) []agentcomm.MessageID {
	if t, ok := o.opts.Store.Snapshot().Assistants.Tasks[taskID]; ok && t.Turn > 0 {
		return t.TurnInbox
	}
	return o.loadTurnInbox(taskID)
}

// startTurn records, before the process starts, the start of taskID's next turn and the messages handed to it
// (inbox), and the read-only guarantee it runs under. It is a running→running task_status_changed with Turn/TurnInbox — the record kind that already marks
// a task's progress, which an older binary still reads — never a new kind it would reject. turn-inbox.json is
// refreshed as a cache of the same list. ok=false: the task is no longer running (cancelled meanwhile) or the
// journal failed (handleTaskOperationErr ends the run); the turn must not start.
func (o *Orchestrator) startTurn(taskID assistants.TaskID, inbox []agentcomm.MessageID) (turn uint64, ok bool) {
	t, found := o.opts.Store.Snapshot().Assistants.Tasks[taskID]
	if !found {
		return 0, false
	}
	turn = t.Turn + 1
	ids := make([]string, len(inbox))
	for i, id := range inbox {
		ids[i] = string(id)
	}
	eff, err := taskStatusChangeEffect(taskID, state.TaskStatusChangedPayload{
		Status: string(assistants.StatusRunning), Turn: turn, TurnInbox: ids,
		ReadonlyEnforcement: o.currentReadonlyEnforcement(t.Task),
	})
	if err != nil {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "assistants: record the turn: "+err.Error())
		return 0, false
	}
	op := state.Operation{
		ID:      fmt.Sprintf("task-turn:%s:%d:%s", taskID, turn, randomOpSuffix()),
		Kind:    "start_turn",
		Expect:  state.Preconditions{TaskStatus: map[string]assistants.TaskStatus{string(taskID): assistants.StatusRunning}},
		Effects: []state.Effect{eff},
	}
	if _, err := o.opts.Store.ApplyRunOperation(op, nil); err != nil {
		o.handleTaskOperationErr(err)
		return 0, false
	}
	if err := o.saveTurnInbox(taskID, inbox); err != nil {
		log.Printf("assistants: task %s: write the turn-inbox cache: %v", taskID, err)
	}
	return turn, true
}

// turnInboxFileName is a cache of the latest unfinished turn's input (state.TaskState.TurnInbox) for a human
// inspecting the task directory; afm reads it only for a journal written before turns were recorded.
const turnInboxFileName = "turn-inbox.json"

// saveTurnInbox writes ids as the turn-inbox cache (or removes it when there are none).
func (o *Orchestrator) saveTurnInbox(taskID assistants.TaskID, ids []agentcomm.MessageID) error {
	if len(ids) == 0 {
		return o.clearTurnInbox(taskID)
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return memory.AtomicWrite(filepath.Join(o.taskDir(taskID), turnInboxFileName), raw)
}

// clearTurnInbox removes the turn-inbox cache: its turn ran to the end.
func (o *Orchestrator) clearTurnInbox(taskID assistants.TaskID) error {
	err := os.Remove(filepath.Join(o.taskDir(taskID), turnInboxFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// loadTurnInbox reads the turn-inbox file; a missing or unreadable file means none.
func (o *Orchestrator) loadTurnInbox(taskID assistants.TaskID) []agentcomm.MessageID {
	raw, err := os.ReadFile(filepath.Join(o.taskDir(taskID), turnInboxFileName))
	if err != nil {
		return nil
	}
	var ids []agentcomm.MessageID
	if json.Unmarshal(raw, &ids) != nil {
		return nil
	}
	return ids
}
