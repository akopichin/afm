package executor

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/akopichin/afm/pkg/redact"
)

// StreamExtensionEnv asks a built-in wrapper to stream a helper's work live (messages, tool starts and finishes,
// an explicit final answer) instead of one buffered result. Only RunHelperAgent with an OnActivity consumer sets it.
const StreamExtensionEnv = "AFM_STREAM_EXTENSION"

// ActivityKind says what one ActivityEvent observed. The empty kind is a plain action line (Tool and Detail
// only), produced for adapters that give no tool id.
type ActivityKind string

const (
	KindMessage            ActivityKind = "message"
	KindToolStarted        ActivityKind = "tool_started"
	KindToolFinished       ActivityKind = "tool_finished"
	KindInvocationFinished ActivityKind = "invocation_finished"
)

// ActivityOutcome is how a tool or a whole process call ended. A tool whose end was never seen has no outcome.
type ActivityOutcome string

const (
	OutcomeCompleted   ActivityOutcome = "completed"
	OutcomeFailed      ActivityOutcome = "failed"
	OutcomeInterrupted ActivityOutcome = "interrupted"
	OutcomeTimedOut    ActivityOutcome = "timed_out"
	OutcomeUsageLimit  ActivityOutcome = "usage_limit"
)

// ActivityEvent is one observation of a helper's work, redacted. Events of one item (a message or a tool) are
// revisions: a later Revision replaces an earlier one, and each event is complete enough to render on its own.
type ActivityEvent struct {
	Kind ActivityKind
	// InvocationID is the call's job ID, or an executor-local "inv-…" when jobs are off. Retries, Continue and
	// watcher passes are separate calls, so provider item IDs of different calls never meet.
	InvocationID string
	// ItemID is "msg:…" for a message and "tool:<provider id>" for a tool; empty for a plain action line and for
	// invocation_finished.
	ItemID   string
	Revision int // grows by one per published event of the item
	// Partial marks a message snapshot that may still grow. LiveOnly marks a partial snapshot that is for the live
	// stream only and must not be stored.
	Partial  bool
	LiveOnly bool
	// Final marks the message revision whose text was submitted as the final answer; the answer itself is
	// validated by the caller as before.
	Final   bool
	Tool    string // tool_started/tool_finished: the same snapshot in both
	Detail  string // message text, or the tool's detail; tool output is never published
	Outcome ActivityOutcome
	// ExitCode is set only when the adapter reported one.
	ExitCode   *int
	StartedAt  time.Time // only when the executor saw the start
	FinishedAt time.Time
}

const (
	// partialLiveInterval and partialDurableInterval bound how often a still-growing message is published.
	partialLiveInterval    = time.Second
	partialDurableInterval = 5 * time.Second

	// finishDetailLimit caps the command a wrapper repeats on a tool's end; the cut comes after redaction.
	finishDetailLimit = 2000

	subtypeAfmStream = "afm_stream"
	typeSystem       = "system"
	toolFallbackName = "tool"
	typeAfmFinal     = "afm_final"
	toolStructured   = "StructuredOutput"
	streamExtVersion = "1"
)

// activityLine is one stream-json line as the activity normalizer reads it: Claude's shape plus the optional
// afm extension. Fields that could clash with other events stay raw, so an odd line never breaks parsing.
type activityLine struct {
	Type    string           `json:"type"`
	Subtype string           `json:"subtype"`
	Version json.RawMessage  `json:"version"`
	Text    string           `json:"text"`
	ItemID  string           `json:"item_id"`
	Message *activityMessage `json:"message"`
	Afm     json.RawMessage  `json:"afm"`
}

type activityMessage struct {
	Content []activityBlock `json:"content"`
}

type activityBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
}

// activityAfm is the afm field of an assistant or user line.
type activityAfm struct {
	ItemID   string `json:"item_id"`
	Partial  bool   `json:"partial"`
	ExitCode *int   `json:"exit_code"`
	Tool     string `json:"tool"`
	Detail   string `json:"detail"`
}

// parseActivityLine returns nil for anything that is not a JSON object.
func parseActivityLine(line string) *activityLine {
	var l activityLine
	if json.Unmarshal([]byte(line), &l) != nil {
		return nil
	}
	return &l
}

func (l *activityLine) afm() activityAfm {
	var a activityAfm
	if len(l.Afm) > 0 {
		_ = json.Unmarshal(l.Afm, &a)
	}
	return a
}

func (l *activityLine) supportedVersion() bool { return string(l.Version) == streamExtVersion }

// isAnnouncement reports the wrapper's first line declaring the extension.
func (l *activityLine) isAnnouncement() bool {
	return l != nil && l.Type == typeSystem && l.Subtype == subtypeAfmStream && l.supportedVersion()
}

func (l *activityLine) isFinal() bool {
	return l != nil && l.Type == typeAfmFinal && l.supportedVersion()
}

func (l *activityLine) isAssistant() bool {
	return l != nil && l.Type == eventTypeAssistant && l.Message != nil
}

// isPartial reports a snapshot of a message that is still being written; without an item id there is nothing to
// attach it to, so such a line is an ordinary one.
func (l *activityLine) isPartial() bool {
	if !l.isAssistant() {
		return false
	}
	a := l.afm()
	return a.Partial && a.ItemID != ""
}

// messageItem is a message the normalizer has seen: its latest full text and what was published of it.
type messageItem struct {
	id        string
	text      string
	revision  int
	published string // detail of the last published partial, to skip an unchanged held-back view
	lastLive  time.Time
	// lastDurable starts at the item's first appearance: a message becomes durable as a partial only once it has
	// been growing for partialDurableInterval.
	lastDurable time.Time
	durable     string // detail of the last durably published partial
	// pending means the latest text has not been published durably yet: a live-only view keeps it pending.
	pending bool
}

type toolItem struct {
	tool, detail string
	revision     int
	startedAt    time.Time
	finished     bool
}

// activityQueueSize bounds the events waiting for a slow sink; events are already coalesced, so an overflow only
// drops revisions that a later one would replace.
const activityQueueSize = 1024

// activityNormalizer turns the redacted lines of one helper call into ActivityEvents. It normalizes on the reader
// goroutine, but one delivery goroutine calls the sink, so a stuck consumer cannot hide the lines that decide the
// call's outcome. A nil sink only tracks the answer.
type activityNormalizer struct {
	sink         func(ActivityEvent)
	now          func() time.Time
	red          helperRedactor
	truncate     int
	invocationID string
	startedAt    time.Time
	// gate serialises the reader's calls with finish, which runs on another goroutine.
	gate   chan struct{}
	closed bool // guarded by gate: nothing is queued after finish
	// out feeds the delivery goroutine; delivered closes when it returns; abandon is closed when finish gives up
	// on a consumer that does not return.
	out       chan ActivityEvent
	delivered chan struct{}
	abandon   chan struct{}

	messages  map[string]*messageItem
	pending   []*messageItem
	tools     map[string]*toolItem
	hidden    map[string]bool // tool ids whose events stay out of the activity (the native payload tool)
	textCount int
	lastText  string // item of the latest text message: the answer's item when no explicit one is named
}

func newActivityNormalizer(sink func(ActivityEvent), now func() time.Time, red helperRedactor, truncate int, jobID string) *activityNormalizer {
	if jobID == "" {
		jobID = "inv-" + rand.Text()
	}
	n := &activityNormalizer{
		sink: sink, now: now, red: red, truncate: truncate, invocationID: jobID, gate: make(chan struct{}, 1),
		messages: map[string]*messageItem{}, tools: map[string]*toolItem{}, hidden: map[string]bool{},
	}
	if sink != nil {
		n.out = make(chan ActivityEvent, activityQueueSize)
		n.delivered, n.abandon = make(chan struct{}), make(chan struct{})
		go n.deliver()
	}
	return n
}

// deliver is the only caller of the sink, so the sink is never re-entered.
func (n *activityNormalizer) deliver() {
	defer close(n.delivered)
	for {
		select {
		case <-n.abandon:
			return
		case ev, ok := <-n.out:
			if !ok {
				return
			}
			select {
			case <-n.abandon:
				return
			default:
			}
			n.sink(ev)
		}
	}
}

// emit queues ev without blocking; call it with the gate held.
func (n *activityNormalizer) emit(ev ActivityEvent) {
	if n.sink == nil || n.closed {
		return
	}
	ev.InvocationID = n.invocationID
	select {
	case n.out <- ev:
	default:
	}
}

// text records an assistant text block. itemID is the wrapper's own item id; empty means a one-off block.
func (n *activityNormalizer) text(itemID, text string, partial bool) {
	if text == "" {
		return
	}
	id := "msg:" + itemID
	if itemID == "" {
		n.textCount++
		id = "msg:t" + strconv.Itoa(n.textCount)
	}
	it := n.messages[id]
	if it == nil {
		it = &messageItem{id: id, lastDurable: n.now()}
		n.messages[id] = it
	}
	it.text = text
	n.lastText = id
	if partial {
		if !it.pending {
			it.pending = true
			n.pending = append(n.pending, it)
		}
		n.flushDue()
		return
	}
	n.dropPending(it)
	n.publishMessage(it, ActivityEvent{Detail: text})
}

func (n *activityNormalizer) dropPending(it *messageItem) {
	if !it.pending {
		return
	}
	it.pending = false
	for i, p := range n.pending {
		if p == it {
			n.pending = append(n.pending[:i], n.pending[i+1:]...)
			return
		}
	}
}

// publishMessage emits the next revision of it; ev carries the text and the Partial/LiveOnly/Final flags.
func (n *activityNormalizer) publishMessage(it *messageItem, ev ActivityEvent) {
	it.revision++
	ev.Kind, ev.ItemID, ev.Revision = KindMessage, it.id, it.revision
	ev.Detail = n.red.text(ev.Detail)
	n.emit(ev)
}

// flushDue publishes the latest snapshot of every pending message that has waited a live interval.
func (n *activityNormalizer) flushDue() {
	now := n.now()
	for _, it := range slices.Clone(n.pending) {
		if !it.lastLive.IsZero() && now.Sub(it.lastLive) < partialLiveInterval {
			continue
		}
		view, ok := n.heldBack(it.text)
		if !ok || view == "" || view == it.published {
			continue // nothing new may be shown yet; the snapshot stays pending
		}
		liveOnly := now.Sub(it.lastDurable) < partialDurableInterval
		if !liveOnly {
			it.lastDurable, it.durable = now, view
			if view == it.text {
				n.dropPending(it)
			}
		}
		it.lastLive, it.published = now, view
		n.publishMessage(it, ActivityEvent{Detail: view, Partial: true, LiveOnly: liveOnly})
	}
}

// heldBack is the part of a snapshot that is safe to show while the message grows: the tail that could be the
// start of a secret is withheld. ok is false when redaction has no secret list to judge by.
func (n *activityNormalizer) heldBack(text string) (view string, ok bool) {
	if n.red.secrets == nil {
		return text, n.red.redact == nil
	}
	return redact.TrimSecretPrefix(text, withJSONSpellings(n.red.secrets())), true
}

// toolUse records a tool_use block; tool and detail come from contentToAction.
func (n *activityNormalizer) toolUse(id, name, tool, detail string) {
	if name == toolStructured {
		n.hidden[id] = true
		return
	}
	tool, detail = n.red.text(tool), n.red.text(detail)
	if id == "" {
		n.emit(ActivityEvent{Tool: tool, Detail: detail})
		return
	}
	if _, seen := n.tools[id]; seen {
		return
	}
	now := n.now()
	n.tools[id] = &toolItem{tool: tool, detail: detail, revision: 1, startedAt: now}
	n.emit(ActivityEvent{Kind: KindToolStarted, ItemID: "tool:" + id, Revision: 1, Tool: tool, Detail: detail, StartedAt: now})
}

// toolResult records the end of a tool. hint carries the wrapper's own tool/detail for a finish without a start.
func (n *activityNormalizer) toolResult(id string, isError bool, hint activityAfm) {
	if id == "" || n.hidden[id] {
		return
	}
	t := n.tools[id]
	if t != nil && t.finished {
		return
	}
	if t == nil {
		tool := hint.Tool
		if tool == "" {
			tool = toolFallbackName
		}
		t = &toolItem{tool: n.red.text(tool), detail: cutUTF8(n.red.text(hint.Detail), finishDetailLimit)}
		n.tools[id] = t
	}
	t.finished = true
	t.revision++
	outcome := OutcomeCompleted
	if isError || (hint.ExitCode != nil && *hint.ExitCode != 0) {
		outcome = OutcomeFailed
	}
	n.emit(ActivityEvent{
		Kind: KindToolFinished, ItemID: "tool:" + id, Revision: t.revision, Tool: t.tool, Detail: t.detail,
		Outcome: outcome, ExitCode: hint.ExitCode, StartedAt: t.startedAt, FinishedAt: n.now(),
	})
}

// finish ends the call's activity in one fixed order: the last pending snapshot, the revision that marks the
// final answer, then invocation_finished. It waits for the consumer to receive them, but only for
// activityFinishWait: a consumer that does not return is abandoned and gets nothing more.
func (n *activityNormalizer) finish(runErr error, finalItem func() string) {
	if n.sink == nil {
		return
	}
	go func() {
		select {
		case n.gate <- struct{}{}:
		case <-n.abandon:
			return
		}
		defer func() { <-n.gate }()
		n.finishLocked(runErr, finalItem())
	}()
	timer := time.NewTimer(activityFinishWait)
	defer timer.Stop()
	select {
	case <-n.delivered:
	case <-timer.C:
		close(n.abandon)
	}
}

// discard stops the delivery goroutine of a call whose process never started: there is no invocation to report.
// No reader runs then, so the gate is free.
func (n *activityNormalizer) discard() {
	if n.sink == nil {
		return
	}
	n.gate <- struct{}{}
	n.closed = true
	close(n.out)
	<-n.gate
	<-n.delivered
}

// finishLocked runs with the gate held. Text that never completed is shown without a possible secret start,
// or not at all when redaction cannot tell where one would start.
func (n *activityNormalizer) finishLocked(runErr error, finalItem string) {
	unfinished := map[*messageItem]bool{}
	for _, it := range slices.Clone(n.pending) {
		n.dropPending(it)
		unfinished[it] = true
		if view, ok := n.heldBack(it.text); ok && view != "" && view != it.durable {
			n.publishMessage(it, ActivityEvent{Detail: view})
		}
	}
	if it := n.messages[finalItem]; it != nil && runErr == nil {
		text, ok := it.text, true
		if unfinished[it] {
			text, ok = n.heldBack(it.text)
		}
		if ok && text != "" {
			n.publishMessage(it, ActivityEvent{Detail: text, Final: true})
		}
	}
	n.emit(ActivityEvent{Kind: KindInvocationFinished, Outcome: callOutcome(runErr), StartedAt: n.startedAt, FinishedAt: n.now()})
	n.closed = true
	close(n.out)
}

// cutUTF8 cuts s to at most limit bytes without splitting a rune.
func cutUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

// callOutcome maps how runJob ended to the invocation's outcome.
func callOutcome(runErr error) ActivityOutcome {
	var limit *UsageLimitError
	switch {
	case runErr == nil:
		return OutcomeCompleted
	case errors.Is(runErr, ErrUserInterrupted):
		return OutcomeInterrupted
	case errors.Is(runErr, ErrIdleTimeout):
		return OutcomeTimedOut
	case errors.As(runErr, &limit):
		return OutcomeUsageLimit
	default:
		return OutcomeFailed
	}
}

// answerItem is the message item the final answer came from, empty when it did not come from a message (the
// native payload) or there is no answer.
func (n *activityNormalizer) answerItem(a *finalAnswer) string {
	switch {
	case a.structured || strings.TrimSpace(a.answer()) == "":
		return ""
	case a.declared && a.extItemID != "":
		return "msg:" + a.extItemID
	case a.declared:
		return "" // the wrapper named no item: no progress message is the answer
	}
	return n.lastText
}

// toolResults records the tool_result blocks of a user line.
func (n *activityNormalizer) toolResults(l *activityLine) {
	if l == nil || l.Type != eventTypeUser || l.Message == nil {
		return
	}
	hint := l.afm()
	for _, b := range l.Message.Content {
		if b.Type == contentTypeToolResult {
			n.toolResult(b.ToolUseID, b.IsError, hint)
		}
	}
}

// textOf is the text of the first text block of an assistant line.
func textOf(l *activityLine) string {
	for _, b := range l.Message.Content {
		if b.Type == contentTypeText {
			return b.Text
		}
	}
	return ""
}

// activityFinishWait bounds the end-of-call publication when the activity consumer is stuck; a variable so tests
// can shorten it.
var activityFinishWait = 2 * time.Second
