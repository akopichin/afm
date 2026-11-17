package mcpserver

import (
	"context"
	"encoding/base64"
	"errors"
	"image"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	// Blank imports register the decoders whose formats an image artifact may use.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/contracts/activity"
	"github.com/akopichin/afm/contracts/control"
	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/server"
)

// Artifact kinds. A client never names a path: it passes an opaque ID that resolves only to one of these.
const (
	kindPlan    = "plan"
	kindSummary = "summary"
	kindImage   = "image"
	// kindDeclared is a "./" artifact a stage declares in flow.yaml; the name is the declared name, resolved
	// against the current registry on every read.
	kindDeclared = "declared"

	planFile  = "plan.md"
	parentDir = ".."
)

const (
	defaultTextPage = 32 << 10
	maxTextPage     = 64 << 10
	// maxTextFile bounds what is read to redact and page; a larger file is not a plan or summary a model can use.
	maxTextFile = 4 << 20
	maxInline   = 1 << 20
	// maxImageFile and maxImageDim mirror the dashboard artifact route.
	maxImageFile    = 10 << 20
	maxImageDim     = 8192
	maxStageImages  = 20
	maxDeclared     = 50
	artifactsSubdir = "artifacts"
)

var imageTypes = map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif"}

// safeName is one path component of an image artifact, the same charset the dashboard route accepts.
var safeName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

const (
	codeUnavailable = "unavailable"
	codeTooLarge    = "too_large"
	codeNotText     = "not_text"
)

// artifactRef names one readable artifact.
type artifactRef struct {
	ID      string `json:"artifact_id"`
	Kind    string `json:"kind"`
	StageID string `json:"stage_id"`
	Name    string `json:"name"`
	URI     string `json:"uri"`
}

// artifactSpec is the decoded form of an artifact ID.
type artifactSpec struct {
	kind, stageID, name string
	// rel is set for a declared artifact once resolved against the registry.
	rel string
}

func (s *Service) refFor(spec artifactSpec) artifactRef {
	raw := spec.kind + "\x00" + spec.stageID + "\x00" + spec.name
	id := base64.RawURLEncoding.EncodeToString([]byte(raw))
	return artifactRef{ID: id, Kind: spec.kind, StageID: spec.stageID, Name: spec.name, URI: artifactURI(s.runID, id)}
}

// parseArtifactID accepts only IDs that decode to an allowed kind with a valid stage and name.
func parseArtifactID(id string) (artifactSpec, error) {
	raw, err := base64.RawURLEncoding.DecodeString(id)
	parts := strings.Split(string(raw), "\x00")
	if err != nil || len(parts) != 3 {
		return artifactSpec{}, unavailable("unknown artifact_id")
	}
	spec := artifactSpec{kind: parts[0], stageID: parts[1], name: parts[2]}
	if !server.ValidStageID(spec.stageID) {
		return artifactSpec{}, unavailable("unknown artifact_id")
	}
	switch spec.kind {
	case kindPlan:
		spec.name = planFile
	case kindSummary:
		spec.name = activity.SummaryFile
	case kindImage:
		if !safeName.MatchString(spec.name) || strings.Contains(spec.name, "..") {
			return artifactSpec{}, unavailable("unknown artifact_id")
		}
	case kindDeclared:
		if spec.name == "" {
			return artifactSpec{}, unavailable("unknown artifact_id")
		}
	default:
		return artifactSpec{}, unavailable("unknown artifact_id")
	}
	return spec, nil
}

func unavailable(reason string) *ToolError {
	return &ToolError{Code: codeUnavailable, Kind: "not_found", Message: reason}
}

// relPath is the artifact's path under the run directory.
func (a artifactSpec) relPath() string {
	if a.rel != "" {
		return a.rel
	}
	if a.kind == kindImage {
		return path.Join(a.stageID, artifactsSubdir, a.name)
	}
	return path.Join(a.stageID, a.name)
}

// declaredRelPath resolves a declared artifact to its path under the run directory. Only "./" paths inside the
// stage directory qualify; project and absolute paths are not run artifacts, and logs stay unreadable.
func declaredRelPath(stageID, declaredPath string) (string, bool) {
	rest, ok := strings.CutPrefix(declaredPath, "./")
	if !ok || strings.Contains(rest, "\\") {
		return "", false
	}
	clean := path.Clean(rest)
	if clean == "." || clean == parentDir || strings.HasPrefix(clean, parentDir+"/") || path.IsAbs(clean) {
		return "", false
	}
	if reservedPath(clean) {
		return "", false
	}
	return path.Join(stageID, clean), true
}

// reservedSuffixes name the run's internal files, which MCP never serves even when a stage declares them: logs,
// journals, sessions and dialog files.
var reservedSuffixes = []string{".jsonl", ".log", ".session.json", ".question.json", ".answer.json"}

// reservedName reports whether one path component is an internal file or a copy derived from one, such as the
// dialog recovery files "<question>.raw", "<question>.<sha>.raw" and "<question>.<sha>.recovery.json".
func reservedName(part string) bool {
	part = strings.ToLower(part)
	if part == "session.json" || strings.HasPrefix(part, "session.json.") {
		return true
	}
	for _, suffix := range reservedSuffixes {
		if strings.HasSuffix(part, suffix) || strings.Contains(part, suffix+".") {
			return true
		}
	}
	return false
}

// reservedPath reports whether any component of rel is reserved.
func reservedPath(rel string) bool {
	return slices.ContainsFunc(strings.Split(rel, "/"), reservedName)
}

// declaredPath finds the path a stage currently declares for name.
func (s *Service) declaredPath(stageID, name string) (rel, declared string, ok bool) {
	if s.cfg.Stages == nil {
		return "", "", false
	}
	for _, a := range s.cfg.Stages.StageConfigs()[stageID].Artifacts {
		if a.Name != name {
			continue
		}
		if rel, ok = declaredRelPath(stageID, a.Path); ok {
			return rel, a.Path, true
		}
	}
	return "", "", false
}

// stageArtifacts lists what a client can read for one stage: its plan, its summary and its images.
func (s *Service) stageArtifacts(stageID string, status state.StageStatus) []artifactRef {
	if s.cfg.RunDir == "" {
		return nil
	}
	root, err := os.OpenRoot(s.cfg.RunDir)
	if err != nil {
		return nil
	}
	defer root.Close()
	var refs []artifactRef
	for _, spec := range []artifactSpec{{kind: kindPlan, stageID: stageID, name: planFile}, {kind: kindSummary, stageID: stageID, name: activity.SummaryFile}} {
		if listable(root, spec.relPath()) || (spec.kind == kindPlan && status == state.StatusAwaitingApproval) {
			refs = append(refs, s.refFor(spec))
		}
	}
	refs = append(refs, s.declaredRefs(root, stageID)...)
	entries, err := readDirBeneath(root, path.Join(stageID, artifactsSubdir))
	if err != nil {
		return refs
	}
	images := 0
	for _, e := range entries {
		if images == maxStageImages {
			break
		}
		spec := artifactSpec{kind: kindImage, stageID: stageID, name: e.Name()}
		if !safeName.MatchString(e.Name()) || strings.Contains(e.Name(), "..") || !hasImageExt(e.Name()) {
			continue
		}
		if listable(root, spec.relPath()) {
			refs = append(refs, s.refFor(spec))
			images++
		}
	}
	return refs
}

// declaredRefs lists the stage's declared artifacts that exist now as regular files.
func (s *Service) declaredRefs(root *os.Root, stageID string) []artifactRef {
	if s.cfg.Stages == nil {
		return nil
	}
	var refs []artifactRef
	for _, a := range s.cfg.Stages.StageConfigs()[stageID].Artifacts {
		if len(refs) == maxDeclared {
			break
		}
		rel, ok := declaredRelPath(stageID, a.Path)
		if !ok || a.Name == "" {
			continue
		}
		if listable(root, rel) {
			refs = append(refs, s.refFor(artifactSpec{kind: kindDeclared, stageID: stageID, name: a.Name}))
		}
	}
	return refs
}

// listable reports whether openArtifact accepts rel now; a read opens it again.
func listable(root *os.Root, rel string) bool {
	f, _, err := openArtifact(root, rel)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func hasImageExt(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif":
		return true
	}
	return false
}

// lookupHook is a test seam that runs once the directory parts[:i+1] of rel is held by descriptor.
var lookupHook func(rel string, i int)

// openBeneath opens rel under root one component at a time, each relative to the descriptor of its parent and
// refusing symlinks. A component renamed or swapped during the walk cannot redirect it, which no pathname recheck
// can promise.
func openBeneath(root *os.Root, rel string, wantDir bool) (*os.File, error) {
	parts := strings.Split(rel, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == parentDir || strings.ContainsAny(part, "\\\x00") {
			return nil, errors.New("invalid artifact path")
		}
	}
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	last := len(parts) - 1
	for i, part := range parts[:last] {
		next, err := openAt(dir, part, true)
		_ = dir.Close()
		if err != nil {
			return nil, err
		}
		dir = next
		if lookupHook != nil {
			lookupHook(rel, i)
		}
	}
	defer dir.Close()
	return openAt(dir, parts[last], wantDir)
}

// readDirBeneath lists the directory rel through openBeneath, sorted by name like fs.ReadDir.
func readDirBeneath(root *os.Root, rel string) ([]fs.DirEntry, error) {
	dir, err := openBeneath(root, rel, true)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, err
}

// openArtifact opens the regular, non-reserved file rel names. The read then comes from the file the descriptor
// pinned, whatever is renamed afterwards; the open does not block on a FIFO.
func openArtifact(root *os.Root, rel string) (*os.File, fs.FileInfo, error) {
	if reservedPath(rel) {
		return nil, nil, errors.New("reserved file")
	}
	f, err := openBeneath(root, rel, false)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("not a regular file")
	}
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, info, nil
}

// readRunFile reads a whole text artifact under limit through openArtifact.
func readRunFile(root *os.Root, rel string, limit int64) ([]byte, error) {
	f, info, err := openArtifact(root, rel)
	if err != nil {
		return nil, unavailable("the artifact does not exist")
	}
	defer f.Close()
	if info.Size() > limit {
		return nil, &ToolError{Code: codeTooLarge, Kind: codeInvalid, Message: "the artifact is too large to read as text"}
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, &ToolError{Code: codeTooLarge, Kind: codeInvalid, Message: "the artifact is too large to read as text"}
	}
	return raw, nil
}

// artifactContent is one page of a readable artifact.
type artifactContent struct {
	Ref artifactRef
	// Text is set for plan, summary and any text artifact; Image for pictures.
	Text       string
	Offset     int
	NextOffset *int
	Total      int
	Revision   string
	Redacted   bool
	Image      []byte
	ImageType  string
	ImageSize  int64
}

// readArtifact resolves id and returns one page (maxBytes, default 32 KiB, at most 64 KiB) of it starting at
// offset. Text goes through secret redaction before it is paged, so a secret split across pages cannot leak.
func (s *Service) readArtifact(ctx context.Context, id string, offset, maxBytes int) (artifactContent, error) {
	return s.readArtifactLimit(ctx, id, offset, maxBytes, maxInline)
}

// readArtifactLimit is readArtifact with the largest image it returns as bytes; the tool inlines at most
// maxInline, the resource read serves any approved image.
func (s *Service) readArtifactLimit(ctx context.Context, id string, offset, maxBytes int, imageLimit int64) (artifactContent, error) {
	spec, err := parseArtifactID(id)
	if err != nil {
		return artifactContent{}, err
	}
	out := artifactContent{Ref: s.refFor(spec)}
	isImage := spec.kind == kindImage
	if spec.kind == kindDeclared {
		rel, declared, ok := s.declaredPath(spec.stageID, spec.name)
		if !ok {
			return artifactContent{}, unavailable("unknown artifact_id")
		}
		spec.rel, isImage = rel, hasImageExt(declared)
	}
	if isImage {
		return s.readImage(spec, out, imageLimit)
	}
	if offset < 0 {
		return artifactContent{}, invalidArg("offset must not be negative")
	}
	if maxBytes <= 0 {
		maxBytes = defaultTextPage
	}
	maxBytes = min(maxBytes, maxTextPage)

	raw, revision, err := s.textBytes(ctx, spec)
	if err != nil {
		return artifactContent{}, err
	}
	if !utf8.Valid(raw) {
		return artifactContent{}, &ToolError{Code: codeNotText, Kind: codeInvalid, Message: "the artifact is not valid UTF-8 text"}
	}
	text := raw
	if redacted := []byte(s.redact(string(raw))); string(redacted) != string(raw) {
		text, out.Redacted = redacted, true
	}
	out.Revision, out.Total = revision, len(text)
	start := min(offset, len(text))
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	end := min(start+maxBytes, len(text))
	for end < len(text) && end > start && !utf8.RuneStart(text[end]) {
		end--
	}
	if end == start && start < len(text) {
		return artifactContent{}, invalidArg("max_bytes is smaller than the next character; use at least 4")
	}
	out.Text, out.Offset = string(text[start:end]), start
	if end < len(text) {
		next := end
		out.NextOffset = &next
	}
	return out, nil
}

// textBytes returns the artifact's bytes and, for a plan waiting for approval, the engine's revision of them.
func (s *Service) textBytes(ctx context.Context, spec artifactSpec) (raw []byte, revision string, err error) {
	if spec.kind == kindPlan && s.cfg.Approval != nil && s.stageStatus(spec.stageID) == state.StatusAwaitingApproval {
		// Without a confirmed snapshot the plan is still readable, but no revision is offered to approve.
		if plan, rev, perr := s.reviewedPlan(ctx, spec.stageID); perr == nil {
			return plan, rev, nil
		}
	}
	root, err := s.openRunRoot()
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	raw, err = readRunFile(root, spec.relPath(), maxTextFile)
	return raw, "", err
}

// reviewedPlan is the plan a client can approve: the engine's snapshot of this run and stage, offered only when
// confinedPlan accepts it. approve applies the same checks, so a revision is offered exactly when it is approvable.
func (s *Service) reviewedPlan(ctx context.Context, stageID string) (plan []byte, revision string, err error) {
	snap, err := s.cfg.Approval.ApprovalSnapshot(ctx, stageID)
	if err != nil {
		return nil, "", err
	}
	if snap.RunID != s.runID || snap.StageID != stageID {
		return nil, "", errUnreviewablePlan
	}
	if plan, err = s.confinedPlan(snap); err != nil {
		return nil, "", err
	}
	return plan, string(snap.Revision), nil
}

var errUnreviewablePlan = &ToolError{Code: control.CodeApprovalUnavailable, Kind: codeConflict, Message: "the plan is not a consistent UTF-8 snapshot"}

// confinedPlan returns the snapshot's plan only when a confined read of plan.md has the same bytes and they are
// valid UTF-8. The engine reads the file outside our confinement, and serialization would silently alter invalid
// bytes, so a client could otherwise approve text it never saw.
func (s *Service) confinedPlan(snap control.ApprovalSnapshot) ([]byte, error) {
	root, err := s.openRunRoot()
	if err != nil {
		return nil, errUnreviewablePlan
	}
	defer root.Close()
	raw, err := readRunFile(root, path.Join(snap.StageID, planFile), maxTextFile)
	if err != nil || snap.PlanMarkdown != string(raw) || !utf8.Valid(raw) {
		return nil, errUnreviewablePlan
	}
	return raw, nil
}

// readImage returns the picture's bytes when it is at most inlineLimit; larger images up to maxImageFile come
// back as metadata only.
func (s *Service) readImage(spec artifactSpec, out artifactContent, inlineLimit int64) (artifactContent, error) {
	root, err := s.openRunRoot()
	if err != nil {
		return artifactContent{}, err
	}
	defer root.Close()
	f, info, err := openArtifact(root, spec.relPath())
	if err != nil {
		return artifactContent{}, unavailable("the artifact does not exist")
	}
	defer f.Close()
	if info.Size() > maxImageFile {
		return artifactContent{}, &ToolError{Code: codeTooLarge, Kind: codeInvalid, Message: "the image is too large"}
	}
	cfg, format, err := image.DecodeConfig(f)
	mime, known := imageTypes[format]
	if err != nil || !known {
		return artifactContent{}, &ToolError{Code: "unsupported_type", Kind: codeInvalid, Message: "the file is not a png, jpeg or gif image"}
	}
	if cfg.Width > maxImageDim || cfg.Height > maxImageDim {
		return artifactContent{}, &ToolError{Code: codeTooLarge, Kind: codeInvalid, Message: "the image dimensions are too large"}
	}
	out.ImageType, out.ImageSize = mime, info.Size()
	if info.Size() > inlineLimit {
		return out, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return artifactContent{}, unavailable("the artifact does not exist")
	}
	out.Image, err = io.ReadAll(io.LimitReader(f, inlineLimit+1))
	if err != nil || int64(len(out.Image)) > inlineLimit {
		return artifactContent{}, unavailable("the artifact changed while it was read")
	}
	return out, nil
}

func (s *Service) openRunRoot() (*os.Root, error) {
	if s.cfg.RunDir == "" {
		return nil, unavailable("the run has no directory")
	}
	root, err := os.OpenRoot(s.cfg.RunDir)
	if err != nil {
		return nil, unavailable("the run directory is not readable")
	}
	return root, nil
}

func (s *Service) stageStatus(stageID string) state.StageStatus {
	return s.cfg.Reader.Snapshot().Stages[stageID].Status
}

// artifactReadInput is the argument of afm_artifact_read.
type artifactReadInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"opaque id from afm_run_status"`
	Offset     int    `json:"offset,omitempty" jsonschema:"byte offset of the page; use next_offset of the previous page"`
	MaxBytes   int    `json:"max_bytes,omitempty" jsonschema:"page size in bytes, default 32768, at most 65536"`
}

// textPage is the structured body of a text artifact page.
type textPage struct {
	ArtifactID string `json:"artifact_id"`
	Kind       string `json:"kind"`
	StageID    string `json:"stage_id"`
	Text       string `json:"text"`
	Offset     int    `json:"offset"`
	NextOffset *int   `json:"next_offset"`
	Total      int    `json:"total"`
	// Revision is the engine's approval revision of a plan that is waiting for approval; it is of the original
	// bytes even when Redacted is true.
	Revision string `json:"revision,omitempty"`
	Redacted bool   `json:"redacted,omitempty"`
}

func (c artifactContent) page() textPage {
	return textPage{
		ArtifactID: c.Ref.ID, Kind: c.Ref.Kind, StageID: c.Ref.StageID, Text: c.Text, Offset: c.Offset,
		NextOffset: c.NextOffset, Total: c.Total, Revision: c.Revision, Redacted: c.Redacted,
	}
}

func (s *Service) registerArtifactTool() {
	addTool(s, toolArtifactRead, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in artifactReadInput) (*sdkmcp.CallToolResult, any, error) {
		release, err := s.enter()
		if err != nil {
			return errorResult(err), nil, nil
		}
		defer release()
		content, err := s.readArtifact(ctx, in.ArtifactID, in.Offset, in.MaxBytes)
		if err != nil {
			return errorResult(err), nil, nil
		}
		if content.ImageType == "" {
			return okResult(content.page()), nil, nil
		}
		return imageResult(content), nil, nil
	})
}

// imageResult returns the picture inline when it is small and as a resource link otherwise.
func imageResult(c artifactContent) *sdkmcp.CallToolResult {
	if c.Image != nil {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.ImageContent{Data: c.Image, MIMEType: c.ImageType}}}
	}
	size := c.ImageSize
	return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.ResourceLink{
		URI: c.Ref.URI, Name: c.Ref.Name, MIMEType: c.ImageType, Size: &size,
	}}}
}
