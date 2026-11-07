package accounting

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/akopichin/afm/contracts/agentjobs"
)

// Read model behind the dashboard's "Models & agents" and the stage hook snapshot. It takes plain values
// (usage records, a journal snapshot, stage and participant metadata) and never reads files or run state,
// so every view of one revision comes from the same immutable Facts.

// Agent kinds; they mirror the participant ID prefixes the orchestrator assigns.
const (
	AgentKindStage       = "stage"
	AgentKindHelper      = "helper"
	AgentKindRunAgent    = "run_agent"
	AgentKindVerify      = "verify"
	AgentKindAssistant   = "assistant"
	AgentKindRunOverhead = "run_overhead"
	AgentKindUnknown     = "unknown"
)

// Keys of the buckets that hold work nobody could attribute.
const (
	UnattributedAgentID = "unattributed"
	RunOverheadAgentID  = "run_overhead"
	UnknownModelKey     = "unknown"
)

// assistantIDPrefix starts the Assistant's participant ID ("side:<run>"); alone it stands for the Assistant
// when the metadata does not name it.
const assistantIDPrefix = "side"

const (
	// HealthAbsent: the run has no agent-jobs.jsonl (it predates the journal).
	HealthAbsent Health = "absent"
	// HealthDisabled: accounting is off, so usage.jsonl is not read.
	HealthDisabled Health = "disabled"
)

// Model provenance beyond the collector's observed and hint.
const (
	ModelSourceLegacy = "legacy" // recorded before the source was kept
	ModelSourceMixed  = "mixed"  // rows only: the jobs differ
)

// How an agent row describes its models.
const (
	ModelStateSingle  = "single"
	ModelStateMixed   = "mixed"
	ModelStateUnknown = "unknown"
)

// JobRunning is the status of a job whose process is alive; finished jobs use their agentjobs outcome.
const JobRunning = "running"

// UsageState is the state of usage.jsonl as the caller saw it.
type UsageState string

const (
	UsageStateOK          UsageState = "ok"
	UsageStateUnavailable UsageState = "unavailable"
	UsageStateDisabled    UsageState = "disabled"
)

// StageMeta is a stage of the runtime registry. StartedAt is its first start, zero if it never started.
// StartUnknown marks a stage that ran before its run's timing was recorded (a run migrated from state.json or
// begun by an older afm): its earlier work is unknown whatever StartedAt says.
type StageMeta struct {
	ID           string
	Title        string
	StartedAt    time.Time
	StartUnknown bool
}

// AgentMeta names a participant of the run. The ID is the key; the title is a label. TaskID is the task a
// helper or run agent stands for, so usage that carries only the task finds its participant.
type AgentMeta struct {
	ID     string
	Kind   string
	Title  string
	TaskID string
}

// AnalyticsMeta is what the orchestrator knows and the journals do not.
type AnalyticsMeta struct {
	// Revision changes whenever anything below does; it is part of the cache identity.
	Revision uint64
	// LiveSince is when the process that owns the journal started. An open job older than that died with
	// an earlier process; zero means nothing is alive (an offline read). It does not end with the flow: the
	// Assistant keeps working while the dashboard stays up.
	LiveSince time.Time
	Stages    []StageMeta
	// Agents lists the participants; the Assistant, when the run has one, is the agent of kind assistant.
	Agents []AgentMeta
}

// AnalyticsInput is everything one Facts build reads.
type AnalyticsInput struct {
	Usage []UsageRecord
	// AssistantUsage is the Assistant's own ledger: it stays outside the run total whether or not its job is
	// on the journal.
	AssistantUsage []UsageRecord
	UsageRevision  uint64
	UsageState     UsageState
	Jobs           agentjobs.Snapshot
	Meta           AnalyticsMeta
}

// AgentRef is a participant as the views show it.
type AgentRef struct {
	ID              string
	Kind            string
	Title           string
	OutsideRunTotal bool
}

// ModelRef is a model by channel and model ID. ColorOrdinal is the order in which the model first
// appeared in the run; gaps are normal because models seen only as a configured hint take a number too.
type ModelRef struct {
	Key          string
	Channel      string
	Model        string
	ColorOrdinal int
}

// AnalyticsHealth is the state of the two stores behind the facts.
type AnalyticsHealth struct {
	Usage Health
	Jobs  Health
	// JobsReasons are the journal's machine reasons (no_journal, corrupt_line, ...).
	JobsReasons []string
	// StartFailed counts attempts that never started a process; they are not jobs.
	StartFailed int
}

// Catalog lists what the filters can offer. It ignores the filters themselves.
type Catalog struct {
	Stages []StageMeta
	Agents []AgentRef
	Models []ModelRef
	Scopes CatalogScopes
}

// CatalogScopes tells which scope filters would find anything.
type CatalogScopes struct {
	RunWide      bool
	Unattributed bool
}

type entryKind int

const (
	entryJob entryKind = iota
	entryUsageOnly
	entryStartFailed
)

// entry is one unit of spend or time. A job joins its journal record with its usage; usage without a job
// (legacy runs, a missing journal line) and attempts that never started are kept as entries too, so no
// usage fact is lost.
type entry struct {
	kind        entryKind
	id          string
	title       string
	agentID     string
	stageID     string
	model       string // model key, empty when unknown
	modelSource string
	status      string
	at          time.Time // start of a job, time of the usage record otherwise
	endedAt     time.Time
	offsetMS    *int64
	durationMS  *int64 // measured by the executor; nil when unknown
	running     bool
	usage       entryUsage
	// unjoined is usage that names a job the journal does not have (yet): only its fields attribute it.
	unjoined bool
	order    int
}

// entryUsage is the usage.jsonl side of an entry. Several records of one job are folded.
type entryUsage struct {
	present bool
	metered bool
	priced  bool
	tokens  Tokens
	costUSD float64
}

func (e *entry) outside(f *Facts) bool { return f.agents[e.agentID].OutsideRunTotal }

// unseen is an attempt that never started a process and left no usage: it shows only as a count.
func (e *entry) unseen() bool { return e.kind == entryStartFailed && !e.usage.present }

// Facts is the immutable aggregation of one revision of usage, jobs and metadata.
type Facts struct {
	usageRev, jobsRev, metaRev uint64
	usageState                 UsageState
	journal                    agentjobs.Snapshot // header and health only
	startFailed                int
	stages                     []StageMeta
	stageByID                  map[string]StageMeta
	entries                    []entry
	agents                     map[string]*AgentRef
	agentOrder                 []string
	models                     map[string]*ModelRef
	modelOrder                 []string
	scopes                     CatalogScopes
}

func (f *Facts) UsageRevision() uint64 { return f.usageRev }
func (f *Facts) JobsRevision() uint64  { return f.jobsRev }
func (f *Facts) MetaRevision() uint64  { return f.metaRev }

// Revision is the composite identity of the facts, in the form the API reports.
func (f *Facts) Revision() string {
	return fmt.Sprintf("u%d.j%d.m%d", f.usageRev, f.jobsRev, f.metaRev)
}

func (f *Facts) Health() AnalyticsHealth {
	h := AnalyticsHealth{
		Usage:       Health(f.usageState),
		Jobs:        HealthOK,
		JobsReasons: slices.Clone(f.journal.Health.Reasons),
		StartFailed: f.startFailed,
	}
	switch {
	case f.legacyJournal():
		h.Jobs = HealthAbsent
	case f.journal.Health.Status == agentjobs.HealthUnavailable:
		h.Jobs = HealthUnavailable
	default:
		h.Jobs = HealthOK
	}
	return h
}

func (f *Facts) legacyJournal() bool {
	return slices.Contains(f.journal.Health.Reasons, agentjobs.ReasonNoJournal)
}

func (f *Facts) Catalog() Catalog {
	c := Catalog{Stages: slices.Clone(f.stages), Scopes: f.scopes}
	for _, id := range f.agentOrder {
		c.Agents = append(c.Agents, *f.agents[id])
	}
	for _, key := range f.modelOrder {
		c.Models = append(c.Models, *f.models[key])
	}
	return c
}

// RunCounts are the models and agents of the run total: the Assistant and a model only it used are left out,
// so the numbers match the rows of the unfiltered breakdown.
type RunCounts struct {
	Models int
	Agents int
}

func (f *Facts) Counts() RunCounts {
	models, agents := map[string]bool{}, map[string]bool{}
	for i := range f.entries {
		e := &f.entries[i]
		if e.unseen() || e.outside(f) {
			continue
		}
		agents[e.agentID] = true
		if e.model != "" {
			models[e.model] = true
		}
	}
	return RunCounts{Models: len(models), Agents: len(agents)}
}

// absentJobs stands for a run that has no agent-jobs.jsonl.
func absentJobs() agentjobs.Snapshot {
	return agentjobs.Snapshot{Health: agentjobs.Health{
		Status:  agentjobs.HealthUnavailable,
		Reasons: []string{agentjobs.ReasonNoJournal},
	}}
}

// BuildFacts joins the inputs once. The result shares nothing mutable with them.
func BuildFacts(in AnalyticsInput) *Facts {
	b := &builder{
		in:        in,
		facts:     &Facts{agents: map[string]*AgentRef{}, models: map[string]*ModelRef{}, stageByID: map[string]StageMeta{}},
		metaByID:  map[string]AgentMeta{},
		taskAgent: map[string]string{},
	}
	b.run()
	return b.facts
}

type builder struct {
	in       AnalyticsInput
	facts    *Facts
	metaByID map[string]AgentMeta
	// taskAgent maps a task ID to the participant it runs as, which is not always helper:<task>.
	taskAgent map[string]string
	// seen lists every model key with the time it first appeared, including configured hints.
	seen []modelSighting
}

type modelSighting struct {
	key   string
	at    time.Time
	order int
}

func (b *builder) run() {
	f := b.facts
	f.usageRev, f.jobsRev, f.metaRev = b.in.UsageRevision, b.in.Jobs.Revision, b.in.Meta.Revision
	f.usageState = b.in.UsageState
	if f.usageState == "" {
		f.usageState = UsageStateOK
	}
	jobs := b.in.Jobs
	if jobs.Health.Status == "" {
		jobs = absentJobs()
	}
	f.journal = agentjobs.Snapshot{Header: jobs.Header, Revision: jobs.Revision, Health: agentjobs.Health{
		Status: jobs.Health.Status, Reasons: slices.Clone(jobs.Health.Reasons),
	}}

	f.stages = slices.Clone(b.in.Meta.Stages)
	for _, s := range f.stages {
		f.stageByID[s.ID] = s
	}
	for _, a := range b.in.Meta.Agents {
		b.metaByID[a.ID] = a
		if a.TaskID != "" {
			b.taskAgent[a.TaskID] = a.ID
		}
	}

	usageByJob := map[string][]UsageRecord{}
	for _, rec := range slices.Concat(b.in.Usage, b.in.AssistantUsage) {
		if rec.JobID != "" {
			usageByJob[rec.JobID] = append(usageByJob[rec.JobID], rec)
		}
	}
	claimed := b.collectJobs(jobs, usageByJob)
	b.collectUsageOnly(b.in.Usage, claimed, b.attributeUsage)
	b.collectUsageOnly(b.in.AssistantUsage, claimed, b.attributeAssistantUsage)
	b.settle()
}

// collectJobs turns journal records into job and start-failed entries and returns the job IDs whose
// usage it consumed.
func (b *builder) collectJobs(jobs agentjobs.Snapshot, usageByJob map[string][]UsageRecord) map[string]bool {
	finished := map[string]agentjobs.Record{}
	for _, rec := range jobs.Records {
		if rec.Type == agentjobs.TypeFinished {
			finished[rec.JobID] = rec
		}
	}
	claimed := map[string]bool{}
	for _, rec := range jobs.Records {
		switch rec.Type {
		case agentjobs.TypeStarted:
			e := b.jobEntry(rec, finished[rec.JobID])
			if recs := usageByJob[rec.JobID]; len(recs) > 0 {
				e.usage = foldUsage(recs)
				b.adoptUsageModel(&e, recs)
				claimed[rec.JobID] = true
			}
			b.add(e)
		case agentjobs.TypeStartFailed:
			e := b.baseEntry(rec.JobMeta, entryStartFailed, rec.At)
			if recs := usageByJob[rec.JobID]; len(recs) > 0 {
				e.usage = foldUsage(recs)
				b.adoptUsageModel(&e, recs)
				claimed[rec.JobID] = true
			}
			b.facts.startFailed++
			b.add(e)
		default:
			// Finished lines are joined to their started line above.
		}
	}
	return claimed
}

func (b *builder) jobEntry(rec, end agentjobs.Record) entry {
	e := b.baseEntry(rec.JobMeta, entryJob, rec.At)
	if !rec.Segment.IsZero() {
		offset := rec.Segment.OffsetMS
		e.offsetMS = &offset
	}
	switch {
	case end.Type == agentjobs.TypeFinished:
		e.status = string(end.Outcome)
		e.endedAt = end.At
		d := end.DurationMS
		e.durationMS = &d
	case !b.in.Meta.LiveSince.IsZero() && !rec.At.Before(b.in.Meta.LiveSince):
		e.status, e.running = JobRunning, true
	default:
		// Opened by a process that is gone: the end is unknown, so no timer may keep running.
		e.status = string(agentjobs.OutcomeInterrupted)
	}
	return e
}

// baseEntry fills what the journal's JobMeta says; usage may refine the model later.
func (b *builder) baseEntry(meta agentjobs.JobMeta, kind entryKind, at time.Time) entry {
	e := entry{kind: kind, id: meta.JobID, title: meta.Title, at: at, order: len(b.facts.entries)}
	e.agentID = b.agentForJob(meta)
	e.stageID = firstNonEmpty(meta.RootStageID, meta.StageID)
	if key := modelKey(meta.Channel, meta.Model); key != "" {
		e.model, e.modelSource = key, ModelSourceHint
		b.noteModel(key, meta.Channel, meta.Model, at, e.order)
	}
	return e
}

// adoptUsageModel prefers the model the agent's own stream named over the configured hint. That model is
// seen when its usage was recorded, at the job's end, not back at the job's start: otherwise a model reported
// late would take the colour of one that is already shown.
func (b *builder) adoptUsageModel(e *entry, recs []UsageRecord) {
	for _, rec := range recs {
		key := modelKey(rec.Channel, rec.Model)
		if key == "" {
			continue
		}
		e.model, e.modelSource = key, firstNonEmpty(rec.ModelSource, ModelSourceLegacy)
		seen := e.endedAt
		if seen.IsZero() {
			seen = latest(e.at, usageTime(rec))
		}
		b.noteModel(key, rec.Channel, rec.Model, seen, e.order)
		return
	}
}

// collectUsageOnly keeps usage that has no job in the journal; attribute resolves its agent and stage.
func (b *builder) collectUsageOnly(usage []UsageRecord, claimed map[string]bool, attribute func(UsageRecord) (agentID, stageID string)) {
	for _, rec := range usage {
		if rec.JobID != "" && claimed[rec.JobID] {
			continue
		}
		e := entry{kind: entryUsageOnly, order: len(b.facts.entries), at: usageTime(rec), unjoined: rec.JobID != ""}
		e.usage = foldUsage([]UsageRecord{rec})
		e.agentID, e.stageID = attribute(rec)
		if key := modelKey(rec.Channel, rec.Model); key != "" {
			e.model, e.modelSource = key, firstNonEmpty(rec.ModelSource, ModelSourceLegacy)
			b.noteModel(key, rec.Channel, rec.Model, e.at, e.order)
		}
		b.add(e)
	}
}

func (b *builder) add(e entry) { b.facts.entries = append(b.facts.entries, e) }

// settle places jobs on the timeline, names untitled jobs and fixes the catalog order.
func (b *builder) settle() {
	f := b.facts
	b.dropOverlappingOffsets()

	// Everything below depends on the order of first appearance, not on the order of discovery.
	order := make([]int, len(f.entries))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, c int) int { return f.entries[a].at.Compare(f.entries[c].at) })

	jobNo := map[string]int{}
	for _, i := range order {
		e := &f.entries[i]
		if e.kind == entryJob && e.title == "" {
			jobNo[e.agentID]++
			e.title = fmt.Sprintf("%s · job %d", f.agents[e.agentID].Title, jobNo[e.agentID])
		}
		if e.unseen() {
			continue
		}
		if !slices.Contains(f.agentOrder, e.agentID) {
			f.agentOrder = append(f.agentOrder, e.agentID)
		}
		if e.stageID == "" {
			if f.agents[e.agentID].Kind == AgentKindUnknown {
				f.scopes.Unattributed = true
			} else {
				f.scopes.RunWide = true
			}
		}
	}
	b.assignOrdinals()
	for i := range f.entries {
		e := &f.entries[i]
		if e.model != "" && !e.unseen() && !slices.Contains(f.modelOrder, e.model) {
			f.modelOrder = append(f.modelOrder, e.model)
		}
	}
	slices.SortFunc(f.modelOrder, func(a, c string) int { return f.models[a].ColorOrdinal - f.models[c].ColorOrdinal })
}

// dropOverlappingOffsets withdraws the timeline position of a job that ends after the next process
// segment began: the clock it was placed on was replaced when the run resumed. Its UTC start and its
// duration stay.
func (b *builder) dropOverlappingOffsets() {
	segmentBase := map[int64]bool{}
	for _, rec := range b.in.Jobs.Records {
		if rec.Type == agentjobs.TypeStarted && !rec.Segment.IsZero() {
			segmentBase[rec.Segment.BaseMS] = true
		}
	}
	bases := make([]int64, 0, len(segmentBase))
	for base := range segmentBase {
		bases = append(bases, base)
	}
	slices.Sort(bases)

	baseOf := map[string]int64{}
	for _, rec := range b.in.Jobs.Records {
		if rec.Type == agentjobs.TypeStarted && !rec.Segment.IsZero() {
			baseOf[rec.JobID] = rec.Segment.BaseMS
		}
	}
	for i := range b.facts.entries {
		e := &b.facts.entries[i]
		base, ok := baseOf[e.id]
		if e.kind != entryJob || !ok || e.offsetMS == nil || e.durationMS == nil {
			continue
		}
		next, found := slices.BinarySearch(bases, base)
		if found {
			next++
		}
		if next < len(bases) && *e.offsetMS+*e.durationMS > bases[next] {
			e.offsetMS = nil
		}
	}
}

func (b *builder) noteModel(key, channel, model string, at time.Time, order int) {
	if _, ok := b.facts.models[key]; !ok {
		b.facts.models[key] = &ModelRef{Key: key, Channel: channel, Model: model}
	}
	b.seen = append(b.seen, modelSighting{key: key, at: at, order: order})
}

// assignOrdinals numbers the models by first appearance. A configured hint counts as an appearance, so a
// job that is running on a hint and later reports another model does not renumber its neighbours.
func (b *builder) assignOrdinals() {
	slices.SortStableFunc(b.seen, func(a, c modelSighting) int {
		if d := a.at.Compare(c.at); d != 0 {
			return d
		}
		return a.order - c.order
	})
	next := 0
	assigned := map[string]bool{}
	for _, s := range b.seen {
		if !assigned[s.key] {
			assigned[s.key] = true
			b.facts.models[s.key].ColorOrdinal = next
			next++
		}
	}
}

// agentForJob resolves the agent of a journal record, registering it on first sight.
func (b *builder) agentForJob(meta agentjobs.JobMeta) string {
	if meta.AgentID == "" {
		return b.agentByFields(meta.StageID, meta.RootStageID, meta.TaskID)
	}
	kind := firstNonEmpty(meta.AgentKind, kindFromID(meta.AgentID))
	return b.register(meta.AgentID, kind)
}

// attributeUsage resolves usage without a job to an agent and a stage by the fields it carries.
func (b *builder) attributeUsage(rec UsageRecord) (agentID, stageID string) {
	if rec.Scope == ScopeRunOverhead {
		return b.register(RunOverheadAgentID, AgentKindRunOverhead), ""
	}
	stageID = firstNonEmpty(rec.RootStageID, rec.StageID)
	return b.agentByFields(rec.StageID, rec.RootStageID, rec.TaskID), stageID
}

// attributeAssistantUsage puts the Assistant's ledger on the Assistant whatever stage a record names: the
// production records say "side", which a flow stage may be called too.
func (b *builder) attributeAssistantUsage(UsageRecord) (agentID, stageID string) {
	return b.register(b.assistantID(), AgentKindAssistant), ""
}

// assistantID is the Assistant's participant ID as the metadata names it.
func (b *builder) assistantID() string {
	for _, a := range b.in.Meta.Agents {
		if a.Kind == AgentKindAssistant {
			return a.ID
		}
	}
	return assistantIDPrefix
}

func (b *builder) agentByFields(stageID, rootStageID, taskID string) string {
	if id, ok := b.taskAgent[taskID]; ok {
		return b.register(id, kindFromID(id))
	}
	switch {
	case taskID != "":
		return b.register("helper:"+taskID, AgentKindHelper)
	case firstNonEmpty(rootStageID, stageID) != "":
		return b.register("stage:"+firstNonEmpty(rootStageID, stageID), AgentKindStage)
	default:
		return b.register(UnattributedAgentID, AgentKindUnknown)
	}
}

// register creates the AgentRef once; the metadata adapter overrides kind and title when it knows the ID.
func (b *builder) register(id, kind string) string {
	if _, ok := b.facts.agents[id]; ok {
		return id
	}
	ref := AgentRef{ID: id, Kind: kind}
	if m, ok := b.metaByID[id]; ok {
		ref.Kind = firstNonEmpty(m.Kind, kind)
		ref.Title = m.Title
	}
	if ref.Title == "" {
		ref.Title = b.defaultTitle(ref)
	}
	ref.OutsideRunTotal = ref.Kind == AgentKindAssistant
	b.facts.agents[id] = &ref
	return id
}

func (b *builder) defaultTitle(ref AgentRef) string {
	rest := ref.ID
	if _, after, ok := strings.Cut(ref.ID, ":"); ok {
		rest = after
	}
	switch ref.Kind {
	case AgentKindStage:
		return b.stageTitle(rest)
	case AgentKindVerify:
		stage, _, _ := strings.Cut(rest, "#")
		return "Verify · " + b.stageTitle(stage)
	case AgentKindAssistant:
		return "Assistant"
	case AgentKindRunOverhead:
		return "Run overhead"
	case AgentKindUnknown:
		return "Unattributed"
	default:
		return rest
	}
}

func (b *builder) stageTitle(id string) string {
	if s, ok := b.facts.stageByID[id]; ok && s.Title != "" {
		return s.Title
	}
	return id
}

func kindFromID(id string) string {
	prefix, _, _ := strings.Cut(id, ":")
	switch {
	case id == RunOverheadAgentID:
		return AgentKindRunOverhead
	case prefix == "stage":
		return AgentKindStage
	case prefix == "helper":
		return AgentKindHelper
	case prefix == "run":
		return AgentKindRunAgent
	case prefix == "verify":
		return AgentKindVerify
	case prefix == assistantIDPrefix:
		return AgentKindAssistant
	default:
		return AgentKindUnknown
	}
}

// foldUsage merges the records of one job: tokens and cost add up, and the job counts as metered or
// priced the way the run summary would count it.
func foldUsage(recs []UsageRecord) entryUsage {
	s := summarize(recs)
	return entryUsage{
		present: true,
		metered: s.Metered > 0,
		priced:  s.Priced,
		tokens:  s.Tokens,
		costUSD: s.CostUSD,
	}
}

// modelKey is channel/model, so equal model names of different providers stay apart. No model ID, no key.
func modelKey(channel, model string) string {
	switch {
	case model == "":
		return ""
	case channel == "":
		return model
	default:
		return channel + "/" + model
	}
}

// usageTime is the moment a usage record was written; a missing or broken stamp sorts first.
func usageTime(rec UsageRecord) time.Time {
	for _, s := range []string{rec.StartedAt, rec.FinishedAt} {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func latest(a, c time.Time) time.Time {
	if c.After(a) {
		return c
	}
	return a
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
