package orchestrator

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// publishNotice sends the same payload to live clients and the replay log. Both deliveries are
// best-effort; neither changes stage state.
func (o *Orchestrator) publishNotice(ev bus.Event) {
	o.ui.Publish(ev)
	stagefiles.AppendNotice(o.opts.RunDir, ev.StageID, string(ev.Type), ev.Data)
}

// Usage-limit notice types (live + notices.jsonl, non-FSM). The critical until lives in the FSM journal
// (state.QuotaWait); a notice is best-effort text for the feed and carries only provider, window, source and
// RFC3339 UTC times — never the agent's raw limit message, which may echo a secret.
const (
	eventQuotaWait    bus.EventType = "quota_wait"    // a stage or helper task parked for a usage limit
	eventQuotaReset   bus.EventType = "quota_reset"   // the wait ended, the work resumes
	eventQuotaWarning bus.EventType = "quota_warning" // the provider says the limit is close; nothing stops
)

// eventCompletionMarkerPendingWait (live + notices.jsonl, non-FSM): a turn wrote its completion marker but ended
// on a pending peer wait, so the stage parks instead of completing.
const eventCompletionMarkerPendingWait bus.EventType = "completion_marker_pending_wait"

// completionMarker is a marker file as a turn left it. valid is the completion probe's verdict; present, modTime
// and digest identify the file itself, so an old marker is told from a written one without any clock and
// whether the probe passes does not matter for that.
type completionMarker struct {
	file    string // the marker's file name; "" — the phase has no marker
	valid   bool
	present bool
	modTime time.Time
	digest  [sha256.Size]byte
}

// markerReadLimit caps how much of a marker is hashed: its identity does not need more.
const markerReadLimit = 1 << 20

// readCompletionMarker looks at the marker that ends phase's turns as complete: execution_summary.md on the
// autonomous track, .done on implementation and review, none elsewhere.
func (o *Orchestrator) readCompletionMarker(s flow.Stage, stageDir, phase string) completionMarker {
	switch phase {
	case phaseAutonomous:
		return probeMarker(stageDir, "execution_summary.md", func() error { return stagefiles.CheckAutonomousCompletion(stageDir) })
	case phaseImplementation, phaseReview:
		return probeMarker(stageDir, ".done", func() error { return stagefiles.CheckCompletion(stageDir, o.projectDir(), s) })
	default:
		return completionMarker{}
	}
}

// probeMarker describes the marker file in stageDir. The agent controls that path, so anything but a regular
// file (a FIFO, a link to an endless device) counts as no marker and is neither probed nor read: the read runs
// before every turn and must not block.
func probeMarker(stageDir, file string, probe func() error) completionMarker {
	m := completionMarker{file: file}
	path := filepath.Join(stageDir, file)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return m
	}
	m.valid = probe() == nil
	content, err := os.Open(path)
	if err != nil {
		return m
	}
	defer content.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(content, markerReadLimit)); err != nil {
		return m
	}
	m.present, m.modTime = true, info.ModTime()
	copy(m.digest[:], hash.Sum(nil))
	return m
}

// freshSince reports a marker that is valid now and was written since before: it appeared, or its content or
// mtime changed. Validity alone never makes an old marker fresh.
func (m completionMarker) freshSince(before completionMarker) bool {
	written := !before.present || !m.modTime.Equal(before.modTime) || m.digest != before.digest
	return m.valid && m.present && written
}

// publishCompletionMarkerNotice reports a marker written while the turn ended on peer waits. One notice per set of
// still-open waits of episode: claimed in process, remembered by notices.jsonl, live only after the durable write.
func (o *Orchestrator) publishCompletionMarkerNotice(stageID, marker string, episode []agentcomm.WaitID) {
	mesh := o.opts.Store.Snapshot().Assistants.Mesh
	open := openBatches(mesh, meshStageParticipantID(stageID))
	var pending []agentcomm.WaitID
	ids := make([]string, 0, len(episode))
	for _, id := range episode {
		if w := mesh.Waits[id]; w != nil && stillOpen(w, open) {
			pending = append(pending, id)
			ids = append(ids, string(id))
		}
	}
	if len(pending) == 0 {
		return // every wait ended before the turn exited: nothing is left to report
	}
	episodeKey := strings.Join(ids, ",")
	claim := stageID + "|" + episodeKey
	if _, taken := o.markerNoticed.LoadOrStore(claim, struct{}{}); taken {
		return
	}
	if stagefiles.HasNotice(o.opts.RunDir, stageID, string(eventCompletionMarkerPendingWait), "episode", episodeKey) {
		return
	}
	if o.markerNoticeDecidedHook != nil {
		o.markerNoticeDecidedHook(stageID)
	}
	ev := bus.Event{Type: eventCompletionMarkerPendingWait, StageID: stageID, Data: map[string]any{
		"stage_id": stageID,
		"marker":   marker,
		"episode":  episodeKey,
		"waits":    stageWaitViews(mesh, pending),
		"message": fmt.Sprintf("%s was written while the stage still waits for peers, so the stage waits for them instead of completing: "+
			"it is not completed and verify has not run. Answer the wait, or have the run owner cancel it "+
			"(GET /api/mesh/stages/%s/waits, POST /api/mesh/stages/%s/waits/cancel).", marker, stageID, stageID),
	}}
	if err := stagefiles.WriteNotice(o.opts.RunDir, stageID, string(ev.Type), ev.Data); err != nil {
		o.markerNoticed.Delete(claim) // the next turn with a fresh marker reports the episode again
		log.Printf("WARN: completion-marker notice for stage %q not recorded: %v", stageID, err)
		return
	}
	o.ui.Publish(ev)
}

// quotaTimeLayout renders a moment for people (hook reasons, notice messages): UTC is spelled out so the text
// means the same to a reader in any zone.
const quotaTimeLayout = "2006-01-02 15:04:05 UTC"

// quotaClock formats t for people in UTC.
func quotaClock(t time.Time) string { return t.UTC().Format(quotaTimeLayout) }

// quotaSubject is "claude, five_hour": the provider and, when known, the limit window.
func quotaSubject(provider, window string) string {
	return strings.Join(nonEmpty(provider, window), ", ")
}

func nonEmpty(items ...string) []string {
	var out []string
	for _, s := range items {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// quotaPrefix is "Usage limit (claude, five_hour)" — "Usage limit" alone when nothing is known.
func quotaPrefix(provider, window string) string {
	if subject := quotaSubject(provider, window); subject != "" {
		return "Usage limit (" + subject + ")"
	}
	return "Usage limit"
}

// QuotaWaitMessage is the human text of a park: why the work waits and until when, or why it is paused. It is
// the lifecycle hook reason, the feed notice text and the `afm check` LAST ACTION, so every surface says the
// same; every time carries an explicit UTC.
func QuotaWaitMessage(q *state.QuotaWait) string {
	if q == nil {
		return quotaPrefix("", "")
	}
	prefix := quotaPrefix(q.Provider, q.Window)
	switch {
	case q.PauseReason == state.QuotaPauseExceedsMaxWait:
		return fmt.Sprintf("%s: reset at %s is later than max_wait; paused", prefix, quotaClock(q.ResetAt))
	case q.PauseReason == state.QuotaPauseNoReset:
		return prefix + ": no reset time to wait for; paused"
	case q.PauseReason == state.QuotaPauseMaxWaits:
		return prefix + ": too many consecutive refusals; paused"
	case q.Source == state.QuotaSourceProviderHold:
		return fmt.Sprintf("%s: waiting for another agent's limit until %s", prefix, quotaClock(q.Until))
	case q.Source == state.QuotaSourceProbe:
		return fmt.Sprintf("%s: reset time unknown, next check at %s", prefix, quotaClock(q.Until))
	default:
		return fmt.Sprintf("%s: waiting until %s", prefix, quotaClock(q.Until))
	}
}

// StageQuota is the usage-limit wait a stage shows to people (dashboard StageView, `afm check`): while it waits
// (awaiting_quota) or is paused out of such a wait — an unwaitable limit or a manual Pause. nil otherwise: the quota
// a stage keeps after it went back to work is the series counter's bookkeeping, not something to display.
func StageQuota(s state.StageState) *state.QuotaWait {
	if s.Quota == nil {
		return nil
	}
	if s.Status == state.StatusAwaitingQuota || (s.Status == state.StatusPaused && s.PausedFrom == state.StatusAwaitingQuota) {
		return s.Quota
	}
	return nil
}

// StageQuotaMessage is QuotaWaitMessage for what people see of a stage right now. A stage a human paused out of a
// wait has no pause reason and keeps its old until, but it does not resume at that time: only Continue probes.
func StageQuotaMessage(s state.StageState, q *state.QuotaWait) string {
	if s.Status == state.StatusPaused && q.PauseReason == "" {
		return quotaPrefix(q.Provider, q.Window) + ": paused while waiting; Continue probes now"
	}
	return QuotaWaitMessage(q)
}

// quotaResetMessage is the text of a wait that ended: the work resumes after the reset, or the one probe a
// human's Continue asked for.
func quotaResetMessage(q *state.QuotaWait) string {
	prefix := quotaPrefix(q.Provider, q.Window)
	if q.ManualProbe {
		return prefix + ": probing after a manual continue"
	}
	return prefix + ": the wait is over, resuming"
}

// quotaNoticeData is the payload of quota_wait / quota_reset. Optional keys are omitted when unknown.
func quotaNoticeData(q *state.QuotaWait, message string) map[string]string {
	data := map[string]string{"provider": q.Provider, "message": message}
	put := func(key, value string) {
		if value != "" {
			data[key] = value
		}
	}
	put("window", q.Window)
	put("source", q.Source)
	put("pause_reason", q.PauseReason)
	if !q.Until.IsZero() {
		put("until", q.Until.UTC().Format(time.RFC3339))
	}
	if !q.ResetAt.IsZero() {
		put("reset_at", q.ResetAt.UTC().Format(time.RFC3339))
	}
	return data
}

// publishQuotaNotice announces a stage's (stageID) or helper task's (taskID, stageID = its root stage) quota
// event. A stage notice carries stage_id, a helper notice task_id; both are filed under the stage so the
// stage's feed replays them.
func (o *Orchestrator) publishQuotaNotice(typ bus.EventType, stageID string, taskID assistants.TaskID, data map[string]string) {
	if taskID != "" {
		data["task_id"] = string(taskID)
	} else {
		data["stage_id"] = stageID
	}
	o.publishNotice(bus.Event{Type: typ, StageID: stageID, Data: data})
}

// limitWarningSink is the executor.Config.OnLimitWarning of one agent: stage author or verifier (stageID) or helper
// (taskID). The provider repeats the same allowed_warning on every turn, so the notice is deduped per
// (provider, window, reset): a new window after the reset warns again.
func (o *Orchestrator) limitWarningSink(stageID string, taskID assistants.TaskID) func(executor.LimitWarning) {
	return func(w executor.LimitWarning) {
		key := fmt.Sprintf("%s|%s|%d", w.Provider, w.Window, w.ResetAt.Unix())
		if _, seen := o.quotaWarned.LoadOrStore(key, struct{}{}); seen {
			return
		}
		root := stageID
		if taskID != "" {
			t, ok := o.opts.Store.Snapshot().Assistants.Tasks[taskID]
			if !ok {
				return
			}
			root = t.RootStageID
		}
		o.publishQuotaNotice(eventQuotaWarning, root, taskID, quotaWarningData(w))
	}
}

// quotaWarningData is the payload of quota_warning.
func quotaWarningData(w executor.LimitWarning) map[string]string {
	message := quotaPrefix(w.Provider, w.Window) + ": the limit is close"
	if w.Utilization > 0 {
		message += fmt.Sprintf(" (%.0f%% used)", w.Utilization*100)
	}
	data := map[string]string{"provider": w.Provider}
	if w.Window != "" {
		data["window"] = w.Window
	}
	if !w.ResetAt.IsZero() {
		data["reset_at"] = w.ResetAt.UTC().Format(time.RFC3339)
		message += "; resets at " + quotaClock(w.ResetAt)
	}
	data["message"] = message
	return data
}
