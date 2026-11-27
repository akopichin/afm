package serve

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/fsutil"
	"github.com/akopichin/afm/contracts/worktree"
	"github.com/akopichin/afm/pkg/childenv"
	"github.com/akopichin/afm/pkg/mcpserver"
	"github.com/akopichin/afm/pkg/runregistry"
)

const (
	ModeCore      = "core"
	ModeDashboard = "dashboard"

	maxTaskBytes          = 512 << 10
	maxRequestIDBytes     = 128
	defaultPollInterval   = 500 * time.Millisecond
	defaultReadyTimeout   = 10 * time.Minute
	defaultUncertainAfter = 2 * time.Minute
)

var (
	requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)
	namePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// ErrInvalidRequest wraps every refusal caused by the request itself.
var ErrInvalidRequest = errors.New("invalid start request")

// AdmissionError is a refusal because of what already runs, not because of the request.
type AdmissionError struct {
	Code    string
	Message string
}

func (e *AdmissionError) Error() string { return e.Code + ": " + e.Message }

const (
	CodeLimitStarting = "limit_starting"
	CodeLimitRunning  = "limit_running"
	CodeConflict      = "conflict"
)

// StartRequest is what a client may ask for. Everything resolves through the config allowlist; nothing is a path
// or a command line.
type StartRequest struct {
	MatchingImage bool
	RequestID     string
	Project       string
	// Pipeline names a configured pipeline; Flow names a file in the project's .afm/flows. Exactly one is set.
	Pipeline string
	Flow     string
	Mode     string
	Worktree string
	// NoWorktree runs in the project checkout even when the flow or config enables worktrees.
	NoWorktree bool
	// MCPAllow limits what the run's own MCP offers: groups, or "read". Empty means every group the host allows;
	// a host that allows none gives the run "read".
	MCPAllow []string
	// Task is the starting prompt text.
	Task string
}

// LauncherConfig wires a Launcher; zero durations take the defaults.
type LauncherConfig struct {
	Host      *Host
	Config    Config
	Discovery *Discovery
	// Binary is the afm executable to run.
	Binary string
	// Env is the parent environment; the child gets it filtered through childenv.ForAFMChild.
	Env []string
	// Groups are the MCP groups children may be given; empty means none, so children are read-only.
	Groups []mcpserver.Group
	Now    func() time.Time
	// PollInterval is how often a launch is checked; ReadyTimeout is how long it may stay starting;
	// UncertainAfter is how long a launch of unknown fate may wait for its process to show up before it is lost.
	PollInterval   time.Duration
	ReadyTimeout   time.Duration
	UncertainAfter time.Duration
}

// Launcher starts afm run children and follows each launch through its receipt. It never restarts anything:
// a launch whose fate is unknown is adopted when its process shows up, or marked lost.
type Launcher struct {
	cfg LauncherConfig

	mu     sync.Mutex // serializes admission and spawn
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// reapers collect children; only tests wait for them.
	reapers sync.WaitGroup
	// accepted, when set, runs after the receipt is created and before the child is spawned; tests pause there.
	accepted func(Receipt)
}

// NewLauncher builds a launcher; Close stops its watchers.
func NewLauncher(cfg LauncherConfig) *Launcher {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.ReadyTimeout == 0 {
		cfg.ReadyTimeout = defaultReadyTimeout
	}
	if cfg.UncertainAfter == 0 {
		cfg.UncertainAfter = defaultUncertainAfter
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	l := &Launcher{cfg: cfg}
	l.ctx, l.cancel = context.WithCancel(context.Background()) //nolint:gosec // Close calls cancel
	return l
}

// Close stops following launches. The children keep running: they belong to the user, not to this process.
func (l *Launcher) Close() {
	l.cancel()
	l.wg.Wait()
}

// InstanceIDFor is the process id the host gives a launch's child, so the child can be recognised before it
// reports anything.
func InstanceIDFor(launchID string) string { return launchID[:32] }

func terminalState(state string) bool {
	return state == StateFailed || state == StateLost || state == StateExited
}

// Start accepts a request and spawns its child. The same request_id with the same parameters returns the
// existing receipt; with other parameters it is a conflict. The child is not tied to the caller's context:
// a cancelled request must not kill work that was accepted.
func (l *Launcher) Start(req StartRequest) (Receipt, error) {
	params, err := l.canonical(req)
	if err != nil {
		return Receipt{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// A repeated request is answered from its receipt before anything that may have changed since is looked at
	// again: the flow file, the project list and the host's permissions are checked only for a new launch.
	launchID := LaunchID(req.RequestID)
	existing, err := l.cfg.Host.Receipt(launchID)
	switch {
	case err == nil:
		if len(req.MCPAllow) == 0 {
			params.MCPAllow = existing.Params.MCPAllow // omitted asks for what the host granted when it accepted
		}
		if existing.ParamsHash != ParamsHash(params) {
			return existing, ErrRequestConflict
		}
		return existing, nil
	case !errors.Is(err, fs.ErrNotExist):
		return Receipt{}, err
	}
	if req.MatchingImage && os.Getenv("AFM_DESKTOP_RUNTIME_IMAGE") == "" {
		return Receipt{}, invalidRequest("this host has no matching Desktop image")
	}
	argv, err := l.prepare(req, params)
	if err != nil {
		return Receipt{}, err
	}
	// Resolved once: admission and the receipt must agree, and later edits of the flow or serve.yaml must not
	// change what this launch reserves.
	identity := l.identityOf(params)
	if err := l.admit(identity); err != nil {
		return Receipt{}, err
	}
	if req.Task != "" {
		if err := fsutil.AtomicWriteFile(l.cfg.Host.TaskPath(launchID), []byte(req.Task), stateFileMode); err != nil {
			return Receipt{}, fmt.Errorf("write task: %w", err)
		}
	}
	receipt, _, err := l.cfg.Host.CreateReceipt(req.RequestID, params, identity, l.cfg.Now())
	if err != nil {
		return Receipt{}, err
	}
	if l.accepted != nil {
		l.accepted(receipt)
	}
	return l.spawn(receipt, argv, l.projectPath(params))
}

func invalidRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}

// canonical validates what the request says by itself and builds its typed parameters. Nothing here reads the
// project list, the files or what the host allows now: a repeated request is compared before those.
func (l *Launcher) canonical(req StartRequest) (LaunchParams, error) {
	switch {
	case req.RequestID == "" || len(req.RequestID) > maxRequestIDBytes || !requestIDPattern.MatchString(req.RequestID):
		return LaunchParams{}, invalidRequest("request_id must be 1-%d characters of letters, digits and . _ : -", maxRequestIDBytes)
	case req.Mode != ModeCore && req.Mode != ModeDashboard:
		return LaunchParams{}, invalidRequest("mode must be %q or %q", ModeCore, ModeDashboard)
	case req.Worktree != "" && !namePattern.MatchString(req.Worktree):
		return LaunchParams{}, invalidRequest("worktree %q is not a plain name", req.Worktree)
	case req.NoWorktree && req.Worktree != "":
		return LaunchParams{}, invalidRequest("give either worktree or no_worktree, not both")
	case req.Task != "" && (len(req.Task) > maxTaskBytes || !utf8.ValidString(req.Task) || strings.TrimSpace(req.Task) == ""):
		return LaunchParams{}, invalidRequest("task must be non-blank UTF-8 text of at most %d bytes", maxTaskBytes)
	case req.Pipeline != "" && req.Flow != "":
		return LaunchParams{}, invalidRequest("give either pipeline or flow, not both")
	case req.Pipeline == "" && req.Flow == "":
		return LaunchParams{}, invalidRequest("give a pipeline or a flow")
	case req.Flow != "" && !flowNameValid(req.Flow):
		return LaunchParams{}, invalidRequest("flow %q must be a .yaml or .yml file name inside .afm/flows", req.Flow)
	}
	groups, err := l.childGroups(req.MCPAllow)
	if err != nil {
		return LaunchParams{}, invalidRequest("%v", err)
	}
	return LaunchParams{
		MatchingImage: req.MatchingImage, Project: req.Project, Pipeline: req.Pipeline, Flow: req.Flow, Mode: req.Mode, Worktree: req.Worktree, NoWorktree: req.NoWorktree,
		MCPAllow: groups, TaskSHA256: TaskDigest(req.Task),
	}, nil
}

// prepare checks a new launch against the allowlist and the project as they are now and builds the argv of its
// child.
func (l *Launcher) prepare(req StartRequest, params LaunchParams) ([]string, error) {
	project, ok := l.cfg.Config.Project(req.Project)
	if !ok {
		return nil, invalidRequest("unknown project %q: it is not in serve.yaml", req.Project)
	}
	if len(req.MCPAllow) > 0 {
		for _, g := range params.MCPAllow {
			if g != readOnlyGroup && !slices.Contains(l.cfg.Groups, mcpserver.Group(g)) {
				return nil, invalidRequest("MCP group %q is not allowed by this host", g)
			}
		}
	}
	launchID := LaunchID(req.RequestID)
	argv := []string{
		"run", "--dir", project.Path, "--mode", req.Mode, "--listen-any-port",
		"--instance-id", InstanceIDFor(launchID), "--launch-id", launchID,
		"--mcp-allow", strings.Join(params.MCPAllow, ","),
	}
	if req.Task != "" {
		argv = append(argv, "--task", "@"+l.cfg.Host.TaskPath(launchID))
	}
	if req.Worktree != "" {
		argv = append(argv, "--worktree", req.Worktree)
	}
	if req.NoWorktree {
		argv = append(argv, "--no-worktree")
	}
	if req.Pipeline != "" {
		pipeline, ok := l.cfg.Config.Pipeline(req.Pipeline)
		if !ok {
			return nil, invalidRequest("unknown pipeline %q: it is not in serve.yaml", req.Pipeline)
		}
		argv = append(argv, "--pipeline", pipeline.Source)
		if pipeline.Options != "" {
			argv = append(argv, "--pipeline-options", pipeline.Options)
		}
		return argv, nil
	}
	flowPath := filepath.Join(project.Path, ".afm", "flows", req.Flow)
	if info, err := os.Stat(flowPath); err != nil || !info.Mode().IsRegular() {
		return nil, invalidRequest("flow %q does not exist in the project", req.Flow)
	}
	return append(argv, flowPath), nil
}

// childGroups turns the requested permissions into the normalized list for --mcp-allow: "read" alone, or a
// sorted list of groups. Nothing requested means what the host allows.
func (l *Launcher) childGroups(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return groupsOrRead(l.cfg.Groups), nil
	}
	parsed, err := mcpserver.ParseGroups(strings.Join(requested, ","))
	if err != nil {
		return nil, err
	}
	return groupsOrRead(parsed.Names()), nil
}

// groupsOrRead sorts the groups; no groups at all is the read-only permission.
func groupsOrRead(groups []mcpserver.Group) []string {
	if len(groups) == 0 {
		return []string{readOnlyGroup}
	}
	names := make([]string, len(groups))
	for i, g := range groups {
		names[i] = string(g)
	}
	slices.Sort(names)
	return names
}

func flowNameValid(name string) bool {
	return namePattern.MatchString(name) && (strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml"))
}

func (l *Launcher) projectPath(p LaunchParams) string {
	project, _ := l.cfg.Config.Project(p.Project)
	return project.Path
}

// identityOf resolves the checkout and the run name the child will use, the way afm run resolves them: an
// explicit worktree is its own checkout under the flow's worktree base, otherwise the project root runs the
// flow's declared name. A pipeline's flow is only known to the child, so the pipeline source stands in for it.
func (l *Launcher) identityOf(p LaunchParams) LaunchIdentity {
	root := canonicalPath(l.projectPath(p))
	var doc flowDoc
	if p.Flow != "" {
		doc = readFlowDoc(filepath.Join(root, ".afm", "flows", p.Flow))
	}
	if p.Worktree != "" {
		base, err := engine.WorktreeBase(root, &flow.Flow{RootDir: doc.RootDir})
		if err != nil {
			base = root
		}
		plan, err := worktree.PlanFor(canonicalPath(base), p.Worktree)
		if err != nil {
			return LaunchIdentity{} // the child refuses this name; an unknown identity conflicts with everything
		}
		return LaunchIdentity{Checkout: plan.Path, RunName: p.Worktree}
	}
	if p.Flow != "" {
		return LaunchIdentity{Checkout: root, RunName: doc.Name}
	}
	pipeline, _ := l.cfg.Config.Pipeline(p.Pipeline)
	return LaunchIdentity{Checkout: root, RunName: "pipeline:" + pipeline.Source + "|" + pipeline.Options}
}

// canonicalPath resolves symlinks, so two spellings of one directory compare equal; a path that does not exist
// yet stays as given.
func canonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

// flowDoc is the part of a flow file that decides where its run works.
type flowDoc struct {
	Name    string `yaml:"name"`
	RootDir string `yaml:"root_dir"`
}

// readFlowDoc reads a flow's name and root_dir; an unreadable name falls back to the file path, which only
// ever matches itself.
func readFlowDoc(path string) flowDoc {
	var doc flowDoc
	data, err := os.ReadFile(path)
	if err == nil {
		err = yaml.Unmarshal(data, &doc)
	}
	if err != nil || doc.Name == "" {
		doc.Name = "file:" + path
	}
	return doc
}

// observedIdentity is the identity a child reports in its registry record once it parsed its flow.
func observedIdentity(rec runregistry.Record) (LaunchIdentity, bool) {
	if rec.Project.Root == "" || rec.Project.RunName == "" {
		return LaunchIdentity{}, false
	}
	checkout := rec.Project.Root
	if rec.Project.Worktree != "" {
		checkout = rec.Project.Worktree
	}
	return LaunchIdentity{Checkout: canonicalPath(checkout), RunName: rec.Project.RunName}, true
}

// admit refuses a launch beyond the limits or one whose identity is held by a launch whose process may still run.
// A start that failed gives back its starting slot at once, but its running slot and identity only once its
// process is proven gone. A launch whose identity is unknown conflicts with everything until then.
func (l *Launcher) admit(id LaunchIdentity) error {
	receipts, err := l.cfg.Host.Receipts()
	if err != nil {
		return err
	}
	records, _ := runregistry.List(l.cfg.Discovery.RegistryDir) // only adds identities; unreadable means none
	var starting, running int
	for _, r := range receipts {
		if !r.holdsRuntime() {
			continue
		}
		running++
		if r.startingSlot() {
			starting++
		}
		if l.holdsIdentity(r, records, id) {
			return &AdmissionError{Code: CodeConflict, Message: fmt.Sprintf(
				"launch %s already runs this flow in the same checkout, or its checkout is not known yet; "+
					"give the new run its own worktree", r.LaunchID[:8])}
		}
	}
	limits := l.cfg.Config.Limits
	if starting >= limits.MaxStarting {
		return &AdmissionError{Code: CodeLimitStarting, Message: fmt.Sprintf("%d runs are still starting (max_starting %d)", starting, limits.MaxStarting)}
	}
	if running >= limits.MaxRunning {
		return &AdmissionError{Code: CodeLimitRunning, Message: fmt.Sprintf("%d managed runs are running (max_running %d)", running, limits.MaxRunning)}
	}
	return nil
}

// holdsIdentity compares id with the identity the launch was admitted for and, once its child reported one,
// with what the child actually runs: a pipeline's flow name is only known then.
func (l *Launcher) holdsIdentity(r Receipt, records []runregistry.Record, id LaunchIdentity) bool {
	if r.Identity == id {
		return true
	}
	known := r.Identity != LaunchIdentity{}
	for _, rec := range records {
		if !recordOfLaunch(r, rec) {
			continue
		}
		if observed, ok := observedIdentity(rec); ok {
			known = true
			if observed == id {
				return true
			}
		}
	}
	return !known || id == LaunchIdentity{}
}

// spawn starts the child, records its identity at once and hands it to the reaper and the watcher. A failure
// to start is recorded on the receipt, so the request_id is not reusable and nothing is retried blindly.
func (l *Launcher) spawn(receipt Receipt, argv []string, dir string) (Receipt, error) {
	logFile, err := os.OpenFile(l.cfg.Host.LogPath(receipt.LaunchID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, stateFileMode)
	if err != nil {
		return l.failSpawn(receipt, fmt.Errorf("open log: %w", err))
	}
	defer logFile.Close()
	// exec.Command, not CommandContext: the child outlives the request that asked for it.
	cmd := exec.Command(l.cfg.Binary, argv...)
	cmd.Dir = dir
	cmd.Env = childenv.ForAFMChild(l.cfg.Env)
	if receipt.Params.MatchingImage {
		cmd.Env = append(cmd.Env, "AFM_DOCKER_IMAGE="+os.Getenv("AFM_DESKTOP_RUNTIME_IMAGE"))
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return l.failSpawn(receipt, fmt.Errorf("start %s: %w", l.cfg.Binary, err))
	}
	pid := cmd.Process.Pid
	marker, _ := runregistry.ProcessStart(pid) // empty on platforms without markers; control then refuses to signal
	updated, err := l.cfg.Host.UpdateReceipt(receipt.LaunchID, l.cfg.Now(), func(r *Receipt) {
		r.State, r.PID, r.ProcessStart, r.InstanceID = StateStarting, pid, marker, InstanceIDFor(r.LaunchID)
	})
	// Close does not wait for reapers: the child is meant to outlive the host.
	l.reapers.Go(func() { l.reap(receipt.LaunchID, cmd) })
	l.watch(receipt.LaunchID)
	if err != nil {
		// The child runs but its pid is not on disk; recovery would find it through its early registry record.
		return receipt, fmt.Errorf("record launch: %w", err)
	}
	return updated, nil
}

func (l *Launcher) failSpawn(receipt Receipt, cause error) (Receipt, error) {
	failed, err := l.cfg.Host.UpdateReceipt(receipt.LaunchID, l.cfg.Now(), func(r *Receipt) {
		r.State, r.Error = StateFailed, cause.Error()
	})
	l.removeTask(receipt.LaunchID)
	if err != nil {
		return receipt, errors.Join(cause, err)
	}
	return failed, cause
}

// reap collects the child so it does not stay a zombie, and records how it ended.
func (l *Launcher) reap(launchID string, cmd *exec.Cmd) {
	waitErr := cmd.Wait()
	l.reaped(launchID, cmd.ProcessState.ExitCode(), waitErr)
}

// reaped records the exit of the launch's host-side process together with what its record and endpoint still show
// about its runs. A Docker run whose container still answers outlives that process: the watcher ends that launch
// once the container is gone too.
func (l *Launcher) reaped(launchID string, code int, waitErr error) {
	based, obs, found := l.lastLook(launchID)
	outlives := found && obs.Record.Docker && obs.Liveness == LiveActiveOrphaned
	l.finish(launchID, func(r *Receipt) {
		if found {
			r.mergeObserved(obs.Record, obs.Runtime, based)
		}
		r.ExitCode = &code
		if !outlives {
			l.ended(r, exitMessage("the run exited before it was ready", code, waitErr))
		}
	})
}

// ended records proof that the launch's process is gone; a launch that was never ready failed with message.
func (l *Launcher) ended(r *Receipt, message string) {
	r.ProcessGone = true
	switch {
	case r.State == StateReady:
		r.State = StateExited
	case !terminalState(r.State):
		r.State, r.Error = StateFailed, message
	default:
		// A failed launch keeps the diagnosis it already has.
	}
}

// lastLook judges the launch's own record; based is the receipt's run, read before the probe.
func (l *Launcher) lastLook(launchID string) (based string, obs Observation, found bool) {
	r, err := l.cfg.Host.Receipt(launchID)
	if err != nil {
		return "", Observation{}, false
	}
	rec, ok := l.recordOf(r)
	if !ok {
		return "", Observation{}, false
	}
	return r.RunID, l.cfg.Discovery.Classify(l.ctx, rec), true
}

func exitMessage(prefix string, code int, err error) string {
	if err != nil {
		return fmt.Sprintf("%s (exit code %d); see the launch log", prefix, code)
	}
	return prefix + "; see the launch log"
}

// finish applies a transition to a receipt and removes the task file once the child no longer needs it.
func (l *Launcher) finish(launchID string, fn func(*Receipt)) {
	r, err := l.cfg.Host.UpdateReceipt(launchID, l.cfg.Now(), fn)
	if err == nil && !r.startingSlot() {
		l.removeTask(launchID)
	}
}

func (l *Launcher) removeTask(launchID string) {
	_ = os.Remove(l.cfg.Host.TaskPath(launchID)) // best effort: a leftover file holds only the prompt the client sent
}

// Recover adopts the launches whose process may still run after the previous host stopped and starts watching
// them. A receipt without a pid becomes uncertain: the host may have died before or after spawning.
func (l *Launcher) Recover() error {
	receipts, err := l.cfg.Host.Receipts()
	if err != nil {
		return err
	}
	for _, r := range receipts {
		if !r.holdsRuntime() {
			continue
		}
		if r.State == StateAccepted {
			l.finish(r.LaunchID, func(r *Receipt) { r.State = StateUncertain })
		}
		l.watch(r.LaunchID)
	}
	l.removeOrphanTasks(receipts)
	return nil
}

// removeOrphanTasks deletes prompt files whose request never got a receipt.
func (l *Launcher) removeOrphanTasks(receipts []Receipt) {
	entries, _ := os.ReadDir(filepath.Join(l.cfg.Host.dir, tasksDirName))
	for _, e := range entries {
		launchID := strings.TrimSuffix(e.Name(), ".md")
		if !slices.ContainsFunc(receipts, func(r Receipt) bool { return r.LaunchID == launchID }) {
			l.removeTask(launchID)
		}
	}
}

// watch follows a launch until it ends.
func (l *Launcher) watch(launchID string) {
	l.wg.Go(func() {
		ticker := time.NewTicker(l.cfg.PollInterval)
		defer ticker.Stop()
		for !l.observe(launchID) {
			select {
			case <-ticker.C:
			case <-l.ctx.Done():
				return
			}
		}
	})
}

// observe moves one launch forward from what the registry and the process table show; it reports whether the
// launch is over: ended and its process gone. A failed launch is followed until its process exits.
func (l *Launcher) observe(launchID string) bool {
	r, err := l.cfg.Host.Receipt(launchID)
	if err != nil || !r.holdsRuntime() {
		return true
	}
	rec, haveRecord := l.recordOf(r)
	var obs Observation
	if haveRecord {
		obs = l.cfg.Discovery.Classify(l.ctx, rec)
	}
	alive := obs.Liveness == LiveActive || obs.Liveness == LiveActiveOrphaned
	// A record that has no endpoint yet (the child is still preparing its pipeline) still proves the process.
	early := haveRecord && obs.Liveness == LiveUnreachable &&
		l.cfg.Discovery.processState(rec) == processAlive
	pid, marker := r.PID, r.ProcessStart
	if pid == 0 && haveRecord {
		pid, marker = rec.PID, rec.ProcessStart
	}
	gone := pid != 0 && l.cfg.Discovery.processState(runregistry.Record{PID: pid, ProcessStart: marker}) == processGone

	switch {
	case alive || early:
		l.adopt(r, rec, obs)
	case gone:
		// What the record knows about the runs is saved before the launch ends: the record goes away with it.
		l.finish(launchID, func(cur *Receipt) {
			if haveRecord {
				cur.bindRecord(rec, nil, r.RunID)
			}
			l.ended(cur, "the run exited before it was ready; see the launch log")
		})
	case r.State == StateUncertain && l.cfg.Now().After(r.UpdatedAt.Add(l.cfg.UncertainAfter)):
		l.finish(launchID, func(cur *Receipt) {
			// Decided on the receipt read before: a listing may have found the process since, and then it is not lost.
			if cur.State == StateUncertain && cur.PID == 0 {
				cur.State, cur.Error = StateLost, "no process reported for this launch; it was not restarted"
			}
		})
	default:
		// Nothing to change yet: the next poll looks again.
	}
	return l.expireUnready(launchID)
}

// expireUnready fails a launch that is still starting past the readiness deadline, whether or not its endpoint
// answers, and reports whether the launch is over.
func (l *Launcher) expireUnready(launchID string) bool {
	after, err := l.cfg.Host.Receipt(launchID)
	if err != nil || !after.holdsRuntime() {
		return true
	}
	if after.State == StateStarting && l.cfg.Now().After(after.CreatedAt.Add(l.cfg.ReadyTimeout)) {
		l.timeOut(launchID)
		after, err = l.cfg.Host.Receipt(launchID)
	}
	return err != nil || !after.holdsRuntime()
}

// adopt records what a live run reports: its process identity (for a launch whose pid was never saved), its
// current run and runs directory, and readiness. A launch that already failed keeps its state.
func (l *Launcher) adopt(r Receipt, rec runregistry.Record, obs Observation) {
	ready := obs.Runtime != nil && obs.Runtime.Ready
	l.finish(r.LaunchID, func(cur *Receipt) {
		if !cur.holdsRuntime() {
			// The process ended while it answered: the runs it named are still its runs, but its end stands.
			cur.mergeObserved(rec, obs.Runtime, r.RunID)
			return
		}
		cur.bindRecord(rec, obs.Runtime, r.RunID)
		if terminalState(cur.State) {
			return
		}
		if cur.State != StateReady {
			cur.State = StateStarting
		}
		if ready {
			cur.State = StateReady
		}
	})
}

// timeOut fails a launch that is still starting past its deadline and asks its process to stop. The transition is
// made on the receipt as it is now, so a launch that became ready or exited meanwhile is left alone; the process
// keeps its running slot until it is seen gone. A process that cannot be identified is not signalled.
func (l *Launcher) timeOut(launchID string) {
	message := fmt.Sprintf("the run did not become ready within %s", l.cfg.ReadyTimeout)
	var timedOut bool
	r, err := l.cfg.Host.UpdateReceipt(launchID, l.cfg.Now(), func(r *Receipt) {
		if r.State == StateStarting && l.cfg.Now().After(r.CreatedAt.Add(l.cfg.ReadyTimeout)) {
			r.State, r.Error, timedOut = StateFailed, message, true
		}
	})
	if err != nil || !timedOut {
		return
	}
	l.removeTask(launchID)
	if err := signalProcess(r.PID, r.ProcessStart, syscall.SIGINT); err != nil {
		_, _ = l.cfg.Host.UpdateReceipt(launchID, l.cfg.Now(), func(r *Receipt) {
			if r.State == StateFailed && r.Error == message {
				r.Error += "; it could not be asked to stop: its process could not be identified"
			}
		})
	}
}

// recordOf finds the registry record of the launch's own process.
func (l *Launcher) recordOf(r Receipt) (runregistry.Record, bool) {
	records, err := runregistry.List(l.cfg.Discovery.RegistryDir)
	if err != nil {
		return runregistry.Record{}, false
	}
	for _, rec := range records {
		if recordOfLaunch(r, rec) {
			return rec, true
		}
	}
	return runregistry.Record{}, false
}
