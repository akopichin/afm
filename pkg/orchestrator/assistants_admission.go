package orchestrator

import (
	"fmt"
	"slices"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// topLevelTaskRequest is a request for one new top-level task — what Attach (a helper on a stage) and
// AttachAndSend (a helper or a run agent with its first thread) both admit through admitTopLevelTask, so a
// rule about which task may be created is written once.
type topLevelTaskRequest struct {
	OperationID string
	ProfileID   assistants.ProfileID
	Scope       assistants.Scope // "" = ScopeStage
	StageID     string           // required for ScopeStage, empty for ScopeRun
	Title       string           // "" = the profile's title
	Instruction string
	Mode        assistants.Mode    // "" = advisory
	Trigger     assistants.Trigger // "" = one_shot
	// Origin is who initiated the task; "" = the actor's kind (a human or the side agent). A mesh agent's actor
	// kind is just "agent", so a task one creates names its origin explicitly.
	Origin assistants.Origin
	Actor  state.Actor
	// Skills and SkillsDir are a flow team member's required skills and extra library (absolute); no other
	// request sets them, so a helper or a delegated child never inherits its author's.
	Skills    []string
	SkillsDir string
}

// admittedTask is a top-level task that passed admission: the effects that create it, its initial status and
// quota reservation, and the preconditions its commit re-checks under the store lock.
type admittedTask struct {
	ID      assistants.TaskID
	Scope   assistants.Scope
	Title   string
	Trigger assistants.Trigger
	// Status is the status the task starts in: queued (it runs now), watching (a watcher waits for its first
	// pass), or "" — waiting_parent, when its stage has not started yet.
	Status  assistants.TaskStatus
	Effects []state.Effect
	Expect  state.Preconditions
}

// admitTopLevelTask decides whether req may create a task, from rs and the profile registry: the stage (it
// exists, is not a script stage, has not finished) or the run scope, the profile and its availability, and the
// delegation depth. Every top-level task reserves one unit of collaboration.limits.max_tasks, checked under the
// commit's lock together with the stage's status and the policy revision.
func (o *Orchestrator) admitTopLevelTask(rs state.RunState, req topLevelTaskRequest) (admittedTask, error) {
	scope := normalizeScope(req.Scope)
	if !scope.Valid() {
		return admittedTask{}, attachRejected(assistants.CodeInvalidRequest, "invalid scope %q (want stage or run)", scope)
	}
	stageActive := false
	switch scope {
	case assistants.ScopeStage:
		if req.StageID == "" {
			return admittedTask{}, attachRejected(assistants.CodeInvalidRequest, "a stage id is required for scope=stage")
		}
		st := o.reg().Stage(req.StageID)
		if st == nil {
			return admittedTask{}, attachRejected(assistants.CodeNotFound, "unknown stage %q", req.StageID)
		}
		if st.IsScript() {
			return admittedTask{}, attachRejected(assistants.CodeInvalidRequest, "stage %q is a script stage — helpers are not supported there", req.StageID)
		}
		// A stage that already finished is never checked again for its required helpers: a new requirement
		// would be left with nothing to re-evaluate it (TestAttachRequiredRacesWithComplete). The commit
		// re-checks this under its lock (StageNotTerminal).
		cur := rs.Stages[req.StageID].Status
		if cur == state.StatusDone || cur == state.StatusFailed {
			return admittedTask{}, attachRejected(assistants.CodeInvalidStatus, "stage %q already reached %s", req.StageID, cur)
		}
		stageActive = cur != state.StatusPending
	case assistants.ScopeRun:
		if req.StageID != "" {
			return admittedTask{}, attachRejected(assistants.CodeInvalidRequest, "a run agent belongs to no stage — omit the stage id for scope=run")
		}
	default:
		return admittedTask{}, fmt.Errorf("assistants: unhandled scope %q", scope)
	}

	if o.opts.AssistantsRegistry == nil {
		return admittedTask{}, attachRejected(assistants.CodeFeatureDisabled, "no assistant profiles configured")
	}
	profile, ok := o.opts.AssistantsRegistry.Get(req.ProfileID)
	if !ok {
		return admittedTask{}, attachRejected(assistants.CodeCapabilityUnavailable, "unknown profile %q", req.ProfileID)
	}
	view, _ := profileView(o.opts.AssistantsRegistry, req.ProfileID)
	if !view.Available {
		return admittedTask{}, attachRejected(assistants.CodeCapabilityUnavailable, "%s", view.UnavailableReason)
	}
	// The task's durable profile snapshot remembers the read-only guarantee that backed it at admission, even
	// if the profile's config or availability changes later — like PolicyRev and MaxDepthAtCreate.
	profile.ReadonlyEnforcement = view.ReadonlyEnforcement

	if rs.Assistants.Policy.Blocks(assistants.ComputeDepth(nil)) {
		return admittedTask{}, attachRejected(assistants.CodeDelegationDepthExceeded, "max_depth=%d", rs.Assistants.Policy.MaxDepth)
	}

	profile = o.redactProfile(profile)
	req = req.withDefaults(profile.Title)
	req.Title, req.Instruction = o.RedactSecrets(req.Title), o.RedactSecrets(req.Instruction)
	task := admittedTask{ID: deriveTaskID(req.OperationID), Scope: scope, Title: req.Title, Trigger: req.Trigger}
	payload := state.TaskCreatedPayload{
		ProfileID: string(req.ProfileID), ProfileSnapshot: profile, Title: req.Title, Instruction: req.Instruction,
		Mode: string(req.Mode), Trigger: string(req.Trigger), PolicyRev: rs.Assistants.Policy.Revision,
		MaxDepthAtCreate: rs.Assistants.Policy.MaxDepth, CreatedBy: string(req.createdBy()),
		RootGenerationAtCreate: rs.Assistants.RootGenerations[req.StageID],
		Skills:                 slices.Clone(req.Skills), SkillsDir: req.SkillsDir,
	}
	if scope == assistants.ScopeRun {
		payload.Scope = string(scope)
	}
	created, err := taskCreatedEffect(task.ID, req.StageID, payload)
	if err != nil {
		return admittedTask{}, err
	}
	task.Effects = []state.Effect{created}

	// A run agent and a helper on a running stage start at once; a watcher waits in watching for its collector
	// (fireWatcherTask starts every pass, the first included); a helper on a pending stage waits for it.
	if scope == assistants.ScopeRun || stageActive {
		task.Status = assistants.StatusQueued
		if isWatcherTrigger(task.Trigger) {
			task.Status = assistants.StatusWatching
		}
		statusEff, err := taskStatusEffect(task.ID, task.Status, "")
		if err != nil {
			return admittedTask{}, err
		}
		task.Effects = append(task.Effects, statusEff)
	}

	quotaEff, err := quotaReservedEffect(1)
	if err != nil {
		return admittedTask{}, err
	}
	task.Effects = append(task.Effects, quotaEff)
	task.Expect = state.Preconditions{PolicyRevision: rs.Assistants.Policy.Revision, MaxQuota: o.opts.Config.Collaboration.Limits.MaxTasks}
	if scope == assistants.ScopeStage {
		task.Expect.StageNotTerminal = []string{req.StageID}
	}
	return task, nil
}

// withDefaults fills what a request may omit with what admission gives it: the profile's title, advisory mode
// and a one_shot trigger.
func (req topLevelTaskRequest) withDefaults(profileTitle string) topLevelTaskRequest {
	if req.Title == "" {
		req.Title = profileTitle // a task added without a title is named after its profile, never its bare id
	}
	if req.Mode == "" {
		req.Mode = assistants.ModeAdvisory
	}
	if req.Trigger == "" {
		req.Trigger = assistants.TriggerOneShot
	}
	return req
}

// replayedTopLevelTask answers a top-level attach whose request id already committed, from the committed
// task_created effect: the same task is its answer; another profile, stage, scope, title, instruction, mode or
// trigger under that id is a conflict. An omitted title, mode or trigger is compared with the default admission
// gave it (the title of the profile snapshot the task was created with), never treated as "any". The stored
// title and instruction were redacted when they committed, so they are compared as redacted text.
func (o *Orchestrator) replayedTopLevelTask(op state.CommittedOperation, req topLevelTaskRequest) (assistants.TaskID, error) {
	var p state.TaskCreatedPayload
	target, found, err := op.Effect("task_created", &p)
	if err != nil {
		return "", err
	}
	req = req.withDefaults(p.ProfileSnapshot.Title)
	same := found && assistants.TaskID(target.TaskID) == deriveTaskID(req.OperationID) && target.StageID == req.StageID &&
		p.ProfileID == string(req.ProfileID) && normalizeScope(assistants.Scope(p.Scope)) == normalizeScope(req.Scope) &&
		o.sameRedactedText(p.Instruction, req.Instruction) && o.sameRedactedText(p.Title, req.Title) && p.Mode == string(req.Mode) && p.Trigger == string(req.Trigger) &&
		slices.Equal(p.Skills, req.Skills) && p.SkillsDir == req.SkillsDir
	if !same {
		return "", state.OperationIDConflictError(req.OperationID)
	}
	return assistants.TaskID(target.TaskID), nil
}

// createdBy is the origin recorded on the task: the explicit one, else the actor's kind.
func (req topLevelTaskRequest) createdBy() assistants.Origin {
	if req.Origin != "" {
		return req.Origin
	}
	return assistants.Origin(req.Actor.Kind)
}

func normalizeScope(s assistants.Scope) assistants.Scope {
	if s == "" {
		return assistants.ScopeStage
	}
	return s
}

// redactProfile masks secrets in the human-readable text of a profile before it is frozen into a task's
// journaled snapshot: its title and instructions. The command is left as configured — it is what runs.
func (o *Orchestrator) redactProfile(p assistants.Profile) assistants.Profile {
	p.Title, p.Instructions = o.RedactSecrets(p.Title), o.RedactSecrets(p.Instructions)
	return p
}
