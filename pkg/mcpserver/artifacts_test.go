package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/contracts/control"
	dialog "github.com/akopichin/afm/contracts/mcp"
	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/server"
)

func (h *harness) refID(kind, stage, name string) string {
	return h.svc.refFor(artifactSpec{kind: kind, stageID: stage, name: name}).ID
}

func (h *harness) read(id string, offset, maxBytes int) (artifactContent, error) {
	return h.svc.readArtifact(context.Background(), id, offset, maxBytes)
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got success", code)
	}
	if got := toolError(err).Code; got != code {
		t.Fatalf("code = %s (%v), want %s", got, err, code)
	}
}

func TestArtifactAllowlistRejectsEverythingElse(t *testing.T) {
	h := newHarness(t, "")
	h.writeFile("s1/planning.jsonl", "agent log")
	h.writeFile("s1/planning.stderr.log", "stderr")
	h.writeFile("s1/planning.dialog.jsonl", "dialog")
	h.writeFile("s1/plan.md", "# plan")
	enc := func(raw string) string { return base64.RawURLEncoding.EncodeToString([]byte(raw)) }
	for name, id := range map[string]string{
		"log kind":          enc("log\x00s1\x00planning.jsonl"),
		"stderr as image":   enc("image\x00s1\x00../planning.stderr.log"),
		"traversal stage":   enc("plan\x00../etc\x00x"),
		"traversal image":   enc("image\x00s1\x00..%2f..%2fx"),
		"nested image":      enc("image\x00s1\x00sub/x.png"),
		"not base64":        "%%%",
		"wrong field count": enc("plan\x00s1"),
		"empty":             "",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := h.read(id, 0, 0)
			wantCode(t, err, codeUnavailable)
		})
	}
	if _, err := h.read(h.refID(kindPlan, "s1", ""), 0, 0); err != nil {
		t.Fatalf("a plan must stay readable: %v", err)
	}
}

func TestArtifactRejectsSymlinkAndNonRegular(t *testing.T) {
	h := newHarness(t, "")
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.dir, "s1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(h.dir, "s1", "plan.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(h.dir, "s1", "execution_summary.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.writeFile("real/plan.md", "ok")
	if err := os.Symlink(filepath.Join(h.dir, "real"), filepath.Join(h.dir, "linked")); err != nil {
		t.Fatal(err)
	}

	_, err := h.read(h.refID(kindPlan, "s1", ""), 0, 0)
	wantCode(t, err, codeUnavailable)
	_, err = h.read(h.refID(kindSummary, "s1", ""), 0, 0)
	wantCode(t, err, codeUnavailable)
	_, err = h.read(h.refID(kindPlan, "linked", ""), 0, 0)
	wantCode(t, err, codeUnavailable)
}

func TestArtifactRejectsOversizeAndNonUTF8(t *testing.T) {
	h := newHarness(t, "")
	h.writeFile("s1/plan.md", strings.Repeat("x", maxTextFile+1))
	h.writeFile("s2/plan.md", "valid \xff\xfe bytes")
	_, err := h.read(h.refID(kindPlan, "s1", ""), 0, 0)
	wantCode(t, err, codeTooLarge)
	_, err = h.read(h.refID(kindPlan, "s2", ""), 0, 0)
	wantCode(t, err, codeNotText)
}

func TestArtifactPagesAlignToRuneBoundaries(t *testing.T) {
	h := newHarness(t, "")
	text := strings.Repeat("я", 10) // 20 bytes, two per rune
	h.writeFile("s1/plan.md", text)
	id := h.refID(kindPlan, "s1", "")

	first, err := h.read(id, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(first.Text) || first.Text != "яя" || first.NextOffset == nil || *first.NextOffset != 4 || first.Total != 20 {
		t.Fatalf("first page = %+v", first)
	}
	// An offset inside a rune moves forward to the next boundary instead of cutting it.
	mid, err := h.read(id, 5, 6)
	if err != nil {
		t.Fatal(err)
	}
	if mid.Offset != 6 || mid.Text != "яяя" || !utf8.ValidString(mid.Text) {
		t.Fatalf("mid page = %+v", mid)
	}
	var got strings.Builder
	for off := 0; ; {
		page, err := h.read(id, off, 7)
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString(page.Text)
		if page.NextOffset == nil {
			break
		}
		off = *page.NextOffset
	}
	if got.String() != text {
		t.Fatalf("pages do not rebuild the text: %q", got.String())
	}
	if _, err := h.read(id, -1, 0); err == nil {
		t.Fatal("negative offset must be rejected")
	}
}

func TestArtifactDefaultAndMaxPage(t *testing.T) {
	h := newHarness(t, "")
	h.writeFile("s1/plan.md", strings.Repeat("a", 200<<10))
	id := h.refID(kindPlan, "s1", "")
	page, err := h.read(id, 0, 0)
	if err != nil || len(page.Text) != defaultTextPage {
		t.Fatalf("default page = %d, %v", len(page.Text), err)
	}
	page, err = h.read(id, 0, 1<<20)
	if err != nil || len(page.Text) != maxTextPage {
		t.Fatalf("max page = %d, %v", len(page.Text), err)
	}
}

func TestArtifactRedactionSetsFlagAndSurvivesPageSplit(t *testing.T) {
	h := newHarness(t, "", func(c *Config) {
		c.RedactSecrets = func(s string) string { return strings.ReplaceAll(s, "TOKEN123", "[REDACTED]") }
	})
	h.writeFile("s1/plan.md", "a TOKEN123 b")
	id := h.refID(kindPlan, "s1", "")

	page, err := h.read(id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Text != "a [REDACTED] b" || !page.Redacted {
		t.Fatalf("page = %+v", page)
	}
	// A page boundary inside the secret must not expose any part of it.
	var all strings.Builder
	for off := 0; ; {
		p, err := h.read(id, off, 4)
		if err != nil {
			t.Fatal(err)
		}
		all.WriteString(p.Text)
		if p.NextOffset == nil {
			break
		}
		off = *p.NextOffset
	}
	if strings.Contains(all.String(), "TOKEN") || all.String() != "a [REDACTED] b" {
		t.Fatalf("paged text leaks or differs: %q", all.String())
	}
}

func TestArtifactPlanWaitingForApprovalComesFromSnapshot(t *testing.T) {
	h := newHarness(t, "approve", func(c *Config) {
		c.RedactSecrets = func(s string) string { return strings.ReplaceAll(s, "SECRET", "[REDACTED]") }
	})
	h.run.setStatus("plan", state.StatusAwaitingApproval)
	h.writeFile("plan/plan.md", "reviewed SECRET plan")
	h.approval.snaps["plan"] = control.ApprovalSnapshot{RunID: "run-1", StageID: "plan", Revision: "rev-2", PlanMarkdown: "reviewed SECRET plan"}

	page, err := h.read(h.refID(kindPlan, "plan", ""), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Revision != "rev-2" || page.Text != "reviewed [REDACTED] plan" || !page.Redacted {
		t.Fatalf("page = %+v", page)
	}
	// Once the stage is no longer waiting, the file is what a reader gets.
	h.run.setStatus("plan", state.StatusDone)
	page, err = h.read(h.refID(kindPlan, "plan", ""), 0, 0)
	if err != nil || page.Text != "reviewed [REDACTED] plan" || page.Revision != "" {
		t.Fatalf("after approval: %+v, %v", page, err)
	}
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestArtifactImageInlineAndLink(t *testing.T) {
	h := newHarness(t, "")
	small := pngBytes(t, 4, 4)
	h.writeFile("s1/artifacts/small.png", string(small))
	// Random-looking pixels keep the PNG above the inline limit.
	big := image.NewNRGBA(image.Rect(0, 0, 1000, 1000))
	_, _ = rand.NewChaCha8([32]byte{1}).Read(big.Pix)
	var buf bytes.Buffer
	if err := png.Encode(&buf, big); err != nil {
		t.Fatal(err)
	}
	if buf.Len() <= maxInline {
		t.Fatalf("test image is only %d bytes", buf.Len())
	}
	h.writeFile("s1/artifacts/big.png", buf.String())
	h.writeFile("s1/artifacts/fake.png", "not an image")

	inline, err := h.read(h.refID(kindImage, "s1", "small.png"), 0, 0)
	if err != nil || !bytes.Equal(inline.Image, small) || inline.ImageType != "image/png" {
		t.Fatalf("inline = %+v, %v", inline, err)
	}
	link, err := h.read(h.refID(kindImage, "s1", "big.png"), 0, 0)
	if err != nil || link.Image != nil || link.ImageSize != int64(buf.Len()) {
		t.Fatalf("link = %+v, %v", link, err)
	}
	if res := imageResult(link); res.Content[0].(*sdkmcp.ResourceLink).URI != link.Ref.URI {
		t.Fatalf("link result = %+v", res.Content[0])
	}
	_, err = h.read(h.refID(kindImage, "s1", "fake.png"), 0, 0)
	wantCode(t, err, "unsupported_type")
}

func TestArtifactStageListingAndReadOverMCP(t *testing.T) {
	h := newHarness(t, "")
	h.run.setStatus("s1", state.StatusDone)
	h.writeFile("s1/plan.md", "# the plan")
	h.writeFile("s1/execution_summary.md", "summary")
	h.writeFile("s1/artifacts/a.png", string(pngBytes(t, 2, 2)))
	h.writeFile("s1/artifacts/notes.txt", "not listed")
	h.writeFile("s1/planning.jsonl", "log")
	session := h.connect(t, nil)

	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "afm_run_status"})
	if err != nil {
		t.Fatal(err)
	}
	var st statusResult
	decode(t, res, &st)
	kinds := map[string]string{}
	for _, a := range st.Stages[0].Artifacts {
		kinds[a.Kind] = a.ID
	}
	if len(st.Stages[0].Artifacts) != 3 || kinds[kindPlan] == "" || kinds[kindSummary] == "" || kinds[kindImage] == "" {
		t.Fatalf("artifacts = %+v", st.Stages[0].Artifacts)
	}

	res, err = session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name: "afm_artifact_read", Arguments: map[string]any{"artifact_id": kinds[kindPlan]},
	})
	if err != nil || res.IsError {
		t.Fatalf("read: %v %+v", err, res)
	}
	var page textPage
	decode(t, res, &page)
	if page.Text != "# the plan" || page.NextOffset != nil {
		t.Fatalf("page = %+v", page)
	}

	rr, err := session.ReadResource(context.Background(), &sdkmcp.ReadResourceParams{URI: artifactURI("run-1", kinds[kindPlan])})
	if err != nil || rr.Contents[0].Text != "# the plan" {
		t.Fatalf("resource = %+v, %v", rr, err)
	}
	if _, err := session.ReadResource(context.Background(), &sdkmcp.ReadResourceParams{URI: artifactURI("other-run", kinds[kindPlan])}); err == nil {
		t.Fatal("a resource of another run must not resolve")
	}
}

func TestArtifactPlanResourceNeedsCurrentRevision(t *testing.T) {
	h := newHarness(t, "approve")
	h.run.setStatus("plan", state.StatusAwaitingApproval)
	h.writeFile("plan/plan.md", "# plan")
	h.approval.snaps["plan"] = control.ApprovalSnapshot{RunID: "run-1", StageID: "plan", Revision: "rev:1/a b", PlanMarkdown: "# plan"}
	session := h.connect(t, nil)

	rr, err := session.ReadResource(context.Background(), &sdkmcp.ReadResourceParams{URI: planURI("run-1", "plan", "rev:1/a b")})
	if err != nil || rr.Contents[0].Text != "# plan" {
		t.Fatalf("current revision: %+v, %v", rr, err)
	}
	if _, err := session.ReadResource(context.Background(), &sdkmcp.ReadResourceParams{URI: planURI("run-1", "plan", "old")}); err == nil {
		t.Fatal("a stale revision must not resolve")
	}
}

// declaredStages serves a mutable declaration list, like the runtime registry a dynamic stage joins.
type declaredStages struct {
	mu      sync.Mutex
	configs map[string]server.StageConfig
}

func (d *declaredStages) StageConfigs() map[string]server.StageConfig {
	d.mu.Lock()
	defer d.mu.Unlock()
	return maps.Clone(d.configs)
}

func (d *declaredStages) set(stageID string, arts ...server.DeclaredArtifact) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.configs[stageID] = server.StageConfig{Artifacts: arts}
}

func declaredHarness(t *testing.T, redact func(string) string) (*harness, *declaredStages) {
	t.Helper()
	ds := &declaredStages{configs: map[string]server.StageConfig{}}
	h := newHarness(t, "", func(c *Config) {
		c.Stages = ds
		c.RedactSecrets = redact
	})
	return h, ds
}

func TestArtifactDeclaredListedAndRead(t *testing.T) {
	h, ds := declaredHarness(t, func(s string) string { return strings.ReplaceAll(s, "TOKEN123", "[REDACTED]") })
	h.run.setStatus("s1", state.StatusDone)
	ds.set("s1",
		server.DeclaredArtifact{Name: "report", Path: "./out/report.md"},
		server.DeclaredArtifact{Name: "shot", Path: "./shot.png"},
		server.DeclaredArtifact{Name: "project", Path: "docs/readme.md"},
		server.DeclaredArtifact{Name: "missing", Path: "./nope.md"},
	)
	h.writeFile("s1/out/report.md", "see TOKEN123 here")
	h.writeFile("s1/shot.png", string(pngBytes(t, 2, 2)))
	h.writeFile("docs/readme.md", "outside the run")

	refs := h.svc.stageArtifacts("s1", state.StatusDone)
	got := map[string]bool{}
	for _, r := range refs {
		if r.Kind == kindDeclared {
			got[r.Name] = true
		}
	}
	if len(got) != 2 || !got["report"] || !got["shot"] {
		t.Fatalf("declared refs = %+v", refs)
	}

	c, err := h.read(h.refID(kindDeclared, "s1", "report"), 0, 0)
	if err != nil || c.Text != "see [REDACTED] here" || !c.Redacted {
		t.Fatalf("text = %+v, %v", c, err)
	}
	c, err = h.read(h.refID(kindDeclared, "s1", "shot"), 0, 0)
	if err != nil || c.ImageType != "image/png" || c.Image == nil {
		t.Fatalf("image = %+v, %v", c, err)
	}
	for _, name := range []string{"project", "missing", "undeclared"} {
		_, err = h.read(h.refID(kindDeclared, "s1", name), 0, 0)
		wantCode(t, err, codeUnavailable)
	}
}

func TestArtifactDeclaredFollowsRuntimeRegistry(t *testing.T) {
	h, ds := declaredHarness(t, nil)
	h.writeFile("dyn/result.md", "dynamic")
	id := h.refID(kindDeclared, "dyn", "result")
	_, err := h.read(id, 0, 0)
	wantCode(t, err, codeUnavailable)

	ds.set("dyn", server.DeclaredArtifact{Name: "result", Path: "./result.md"})
	if c, err := h.read(id, 0, 0); err != nil || c.Text != "dynamic" {
		t.Fatalf("after declaring: %+v, %v", c, err)
	}

	ds.set("dyn")
	_, err = h.read(id, 0, 0)
	wantCode(t, err, codeUnavailable)
}

func TestArtifactDeclaredRejectsEscapesAndUnsafeFiles(t *testing.T) {
	h, ds := declaredHarness(t, nil)
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.writeFile("s1/planning.jsonl", "agent log")
	h.writeFile("s1/run.log", "log")
	h.writeFile("secret.md", "other stage level file")
	h.writeFile("real/x.md", "x")
	if err := os.Symlink(outside, filepath.Join(h.dir, "s1", "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(h.dir, "real"), filepath.Join(h.dir, "s1", "dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.dir, "s1", "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		"traversal": "./../secret.md", "deep traversal": "./a/../../secret.md", "absolute": outside,
		"symlink file": "./link.md", "symlink dir": "./dir/x.md", "directory": "./folder",
		"agent log": "./planning.jsonl", "plain log": "./run.log", "stage dir itself": "./", "dot": "./.",
		"backslash": `./..\secret.md`, "no prefix": "secret.md",
	}
	var arts []server.DeclaredArtifact
	for name, p := range paths {
		arts = append(arts, server.DeclaredArtifact{Name: name, Path: p})
	}
	ds.set("s1", arts...)
	for name := range paths {
		t.Run(name, func(t *testing.T) {
			_, err := h.read(h.refID(kindDeclared, "s1", name), 0, 0)
			wantCode(t, err, codeUnavailable)
		})
	}
	for _, r := range h.svc.stageArtifacts("s1", state.StatusDone) {
		if r.Kind == kindDeclared {
			t.Fatalf("unsafe declaration listed: %+v", r)
		}
	}
}

func TestArtifactDeclaredOverMCP(t *testing.T) {
	h, ds := declaredHarness(t, nil)
	h.run.setStatus("s1", state.StatusDone)
	ds.set("s1", server.DeclaredArtifact{Name: "report", Path: "./report.md"})
	h.writeFile("s1/report.md", "# report")
	session := h.connect(t, nil)

	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "afm_run_status"})
	if err != nil {
		t.Fatal(err)
	}
	var st statusResult
	decode(t, res, &st)
	var id string
	for _, a := range st.Stages[0].Artifacts {
		if a.Kind == kindDeclared {
			id = a.ID
		}
	}
	if id == "" {
		t.Fatalf("no declared artifact in %+v", st.Stages[0].Artifacts)
	}
	res, err = session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name: "afm_artifact_read", Arguments: map[string]any{"artifact_id": id},
	})
	if err != nil || res.IsError {
		t.Fatalf("read: %v %+v", err, res)
	}
	var page textPage
	decode(t, res, &page)
	if page.Text != "# report" {
		t.Fatalf("page = %+v", page)
	}
}

// awaitingPlan puts stage "plan" in approval with the same text on disk and in the engine's snapshot.
func awaitingPlan(h *harness, text string) {
	h.run.setStatus("plan", state.StatusAwaitingApproval)
	h.writeFile("plan/plan.md", text)
	h.approval.snaps["plan"] = control.ApprovalSnapshot{RunID: "run-1", StageID: "plan", Revision: "rev-1", PlanMarkdown: text}
}

func TestPlanWithoutConsistentSnapshotOffersNoRevision(t *testing.T) {
	cases := map[string]func(h *harness){
		"snapshot differs from file": func(h *harness) {
			h.approval.snaps["plan"] = control.ApprovalSnapshot{RunID: "run-1", StageID: "plan", Revision: "rev-1", PlanMarkdown: "other"}
		},
		"non-UTF-8 plan": func(h *harness) {
			awaitingPlan(h, "plan \xff")
		},
		"stage directory is a symlink": func(h *harness) {
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "plan.md"), []byte("outside plan"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(h.dir, "plan")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(h.dir, "plan")); err != nil {
				t.Fatal(err)
			}
			h.approval.snaps["plan"] = control.ApprovalSnapshot{RunID: "run-1", StageID: "plan", Revision: "rev-1", PlanMarkdown: "outside plan"}
		},
	}
	for name, tune := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, "approve,stages")
			awaitingPlan(h, "inside plan")
			tune(h)
			session := h.connect(t, nil)

			if page, err := h.read(h.refID(kindPlan, "plan", ""), 0, 0); err == nil && (page.Revision != "" || page.Text == "outside plan") {
				t.Fatalf("read exposed a revision or outside text: %+v", page)
			}
			if _, err := session.ReadResource(context.Background(), &sdkmcp.ReadResourceParams{URI: planURI("run-1", "plan", "rev-1")}); err == nil {
				t.Fatal("plan resource resolved without a consistent snapshot")
			}
			got := actionsOf(t, h.svc.buildStatus(context.Background()), actionApproval)
			if len(got) != 1 || got[0].Revision != "" || slices.Contains(got[0].AllowedActions, toolActionApprove) {
				t.Fatalf("approval action = %+v", got)
			}
		})
	}
}

func TestPlanResourceKeepsRedactedFlag(t *testing.T) {
	h := newHarness(t, "approve", func(c *Config) {
		c.RedactSecrets = func(s string) string { return strings.ReplaceAll(s, "SECRET", "[REDACTED]") }
	})
	awaitingPlan(h, "review SECRET")
	session := h.connect(t, nil)

	rr, err := session.ReadResource(context.Background(), &sdkmcp.ReadResourceParams{URI: planURI("run-1", "plan", "rev-1")})
	if err != nil || rr.Contents[0].Text != "review [REDACTED]" || !redactedFlag(rr.Contents[0]) {
		t.Fatalf("plan resource = %+v, %v", rr, err)
	}
	h.writeFile("s1/notes.txt", "token SECRET")
	h.svc.cfg.Stages = &declaredStages{configs: map[string]server.StageConfig{"s1": {Artifacts: []server.DeclaredArtifact{{Name: "notes", Path: "./notes.txt"}}}}}
	id := h.refID(kindDeclared, "s1", "notes")
	rr, err = session.ReadResource(context.Background(), &sdkmcp.ReadResourceParams{URI: artifactURI("run-1", id)})
	if err != nil || rr.Contents[0].Text != "token [REDACTED]" || !redactedFlag(rr.Contents[0]) {
		t.Fatalf("artifact resource = %+v, %v", rr, err)
	}
}

func TestDeclaredArtifactsHideSessionAndDialogFiles(t *testing.T) {
	h, ds := declaredHarness(t, nil)
	files := []string{"planning.session.json", "planning.q1.question.json", "planning.q1.answer.json", "sub/dialog.jsonl", "sub/x.LOG", "session.json"}
	arts := []server.DeclaredArtifact{{Name: "ok", Path: "./ok.txt"}}
	h.writeFile("s1/ok.txt", "fine")
	for i, f := range files {
		h.writeFile("s1/"+f, "secret")
		arts = append(arts, server.DeclaredArtifact{Name: "f" + string(rune('a'+i)), Path: "./" + f})
	}
	ds.set("s1", arts...)

	for _, ref := range h.svc.stageArtifacts("s1", state.StatusDone) {
		if ref.Kind == kindDeclared && ref.Name != "ok" {
			t.Fatalf("forbidden declared artifact listed: %+v", ref)
		}
	}
	for i := range files {
		_, err := h.read(h.refID(kindDeclared, "s1", "f"+string(rune('a'+i))), 0, 0)
		wantCode(t, err, codeUnavailable)
	}
	if _, err := h.read(h.refID(kindDeclared, "s1", "ok"), 0, 0); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactPageSmallerThanNextRuneIsRejected(t *testing.T) {
	h := newHarness(t, "")
	h.writeFile("s1/plan.md", "яя")
	id := h.refID(kindPlan, "s1", "")
	_, err := h.read(id, 0, 1)
	wantCode(t, err, "invalid")
	page, err := h.read(id, 0, 2)
	if err != nil || page.Text != "я" || page.NextOffset == nil || *page.NextOffset != 2 {
		t.Fatalf("page = %+v, %v", page, err)
	}
}

func TestArtifactOpenRejectsSwappedAndSpecialFiles(t *testing.T) {
	h := newHarness(t, "")
	h.writeFile("s1/plan.md", "plan")
	h.writeFile("s1/secret.log", "log")
	root, err := os.OpenRoot(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// An in-root symlink to a forbidden file is refused.
	if err := os.Symlink("secret.log", filepath.Join(h.dir, "s1", "link.md")); err != nil {
		t.Fatal(err)
	}
	if f, _, err := openArtifact(root, "s1/link.md"); err == nil {
		f.Close()
		t.Fatal("symlink was opened")
	}
	// A file swapped for a symlink once its directory is held, just before its own open, is refused by that open.
	h.writeFile("s1/swap.md", "plain")
	swapped := false
	lookupHook = func(rel string, i int) {
		if rel == "s1/swap.md" && i == 0 {
			swapped = os.Remove(filepath.Join(h.dir, "s1", "swap.md")) == nil &&
				os.Symlink("plan.md", filepath.Join(h.dir, "s1", "swap.md")) == nil
		}
	}
	defer func() { lookupHook = nil }()
	if f, _, err := openArtifact(root, "s1/swap.md"); err == nil {
		f.Close()
		t.Fatal("a file swapped for a symlink before its open was accepted")
	}
	if !swapped {
		t.Fatal("the walk never held s1, so the swap was not exercised")
	}
	lookupHook = nil
	// A FIFO must be rejected without blocking on a missing writer.
	fifo := filepath.Join(h.dir, "s1", "pipe.md")
	if err := mkfifo(fifo); err != nil {
		t.Skipf("no FIFO support: %v", err)
	}
	if f, _, err := openArtifact(root, "s1/pipe.md"); err == nil {
		f.Close()
		t.Fatal("FIFO was opened")
	}
	f, err := openBeneath(root, "s1/pipe.md", false) // must return, not wait for a writer
	if err != nil {
		t.Fatal(err)
	}
	if info, err := f.Stat(); err != nil || info.Mode().IsRegular() {
		t.Fatalf("FIFO descriptor = %v, %v", info, err)
	}
	f.Close()
	if f, _, err := openArtifact(root, "s1/plan.md"); err != nil {
		t.Fatalf("regular file: %v", err)
	} else {
		f.Close()
	}
}

// A device node in place of an artifact is refused like any other file that is not regular.
func TestArtifactRejectsDeviceNode(t *testing.T) {
	h, ds := declaredHarness(t, nil)
	h.writeFile("s1/plan.md", "plan")
	switch err := mknodNull(filepath.Join(h.dir, "s1", "null.md")); {
	case errors.Is(err, syscall.EPERM):
		t.Skipf("this environment may not create device nodes, so the device case is not exercised here: %v", err)
	case errors.Is(err, errors.ErrUnsupported):
		t.Skip("this platform has no device nodes")
	case err != nil:
		t.Fatal(err)
	default:
		// The device node exists.
	}
	root, err := os.OpenRoot(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if f, _, err := openArtifact(root, "s1/null.md"); err == nil {
		f.Close()
		t.Fatal("a device node was opened as an artifact")
	}
	ds.set("s1", server.DeclaredArtifact{Name: "device", Path: "./null.md"})
	_, err = h.read(h.refID(kindDeclared, "s1", "device"), 0, 0)
	wantCode(t, err, codeUnavailable)
	for _, r := range h.svc.stageArtifacts("s1", state.StatusDone) {
		if r.Kind == kindDeclared {
			t.Fatalf("a device node was listed: %+v", r)
		}
	}
}

func TestArtifactImageAboveInlineLimitReadsAsResource(t *testing.T) {
	h := newHarness(t, "")
	big := image.NewNRGBA(image.Rect(0, 0, 1000, 1000))
	_, _ = rand.NewChaCha8([32]byte{2}).Read(big.Pix)
	var buf bytes.Buffer
	if err := png.Encode(&buf, big); err != nil {
		t.Fatal(err)
	}
	h.writeFile("s1/artifacts/big.png", buf.String())
	session := h.connect(t, nil)
	ref := h.svc.refFor(artifactSpec{kind: kindImage, stageID: "s1", name: "big.png"})

	rr, err := session.ReadResource(context.Background(), &sdkmcp.ReadResourceParams{URI: ref.URI})
	if err != nil || !bytes.Equal(rr.Contents[0].Blob, buf.Bytes()) {
		t.Fatalf("oversize image resource: %v", err)
	}
}

func redactedFlag(c *sdkmcp.ResourceContents) bool {
	flag, _ := c.Meta["redacted"].(bool)
	return flag
}

// produceRecoveryFiles makes the dialog code repair the same malformed question twice, as it does when an agent
// rewrites it, and returns the base names of the copies it keeps next to the question in dir.
func produceRecoveryFiles(t *testing.T, dir string) []string {
	t.Helper()
	question := filepath.Join(dir, "planning.q1.question.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"private dialog first", "private dialog second"} {
		if err := os.WriteFile(question, []byte(`{"id":"q1","question":"`+text+`","options":["A"],}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := dialog.FindUnansweredQuestions(dir); err != nil {
			t.Fatal(err)
		}
	}
	var names []string
	for _, pattern := range []string{"*.raw", "*.recovery.json"} {
		found, err := filepath.Glob(question + pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range found {
			names = append(names, filepath.Base(f))
		}
	}
	// "<question>.raw", "<question>.<sha>.raw" and the receipts "<question>.<sha>.recovery.json".
	if len(names) < 3 {
		t.Fatalf("the dialog code kept %v, want raw copies and a recovery receipt", names)
	}
	return names
}

// A declaration cannot make an internal file readable: the dialog code's own recovery copies, at the stage root
// or nested, and derivatives of session and log files stay unlisted and unreadable.
func TestArtifactDeclaredRecoveryAndDerivedFilesStayReserved(t *testing.T) {
	h, ds := declaredHarness(t, nil)
	var paths []string
	for _, name := range produceRecoveryFiles(t, filepath.Join(h.dir, "s1")) {
		paths = append(paths, "./"+name)
	}
	for _, name := range produceRecoveryFiles(t, filepath.Join(h.dir, "s1", "sub")) {
		paths = append(paths, "./sub/"+name)
	}
	for _, rel := range []string{
		"planning.session.json.bak", "session.json.1", "run.log.1", "planning.jsonl.gz", "planning.q2.answer.json.tmp",
		"PLANNING.Q3.QUESTION.JSON.RAW", "notes.session.json.d/readme.md",
	} {
		h.writeFile("s1/"+rel, "private")
		paths = append(paths, "./"+rel)
	}
	h.writeFile("s1/result.json", `{"ok":true}`)
	h.writeFile("s1/data/summary.json", `{"n":1}`)
	arts := []server.DeclaredArtifact{{Name: "result", Path: "./result.json"}, {Name: "summary", Path: "./data/summary.json"}}
	for i, p := range paths {
		arts = append(arts, server.DeclaredArtifact{Name: "internal-" + string(rune('a'+i)), Path: p})
	}
	ds.set("s1", arts...)

	listed := map[string]bool{}
	for _, ref := range h.svc.stageArtifacts("s1", state.StatusDone) {
		listed[ref.Name] = true
	}
	if len(listed) != 2 || !listed["result"] || !listed["summary"] {
		t.Fatalf("listed = %v, want only the two declared JSON artifacts", listed)
	}
	for _, a := range arts[2:] {
		got, err := h.read(h.refID(kindDeclared, "s1", a.Name), 0, 0)
		if err == nil {
			t.Errorf("%s served %q", a.Path, got.Text)
		}
	}
	for name, want := range map[string]string{"result": `{"ok":true}`, "summary": `{"n":1}`} {
		if c, err := h.read(h.refID(kindDeclared, "s1", name), 0, 0); err != nil || c.Text != want {
			t.Errorf("%s = %+v, %v", name, c, err)
		}
	}
}

// A symlink in any component is refused, even when it points at a regular file inside the run.
func TestArtifactRejectsSymlinkInEveryComponent(t *testing.T) {
	for _, link := range []string{"s1", "s1/a", "s1/a/b", "s1/a/b/file.md"} {
		t.Run(link, func(t *testing.T) {
			h, ds := declaredHarness(t, nil)
			h.writeFile("real/s1/a/b/file.md", "in-run text")
			if err := os.MkdirAll(filepath.Join(h.dir, filepath.Dir(link)), 0o755); err != nil {
				t.Fatal(err)
			}
			// The link replaces one component; the rest of the path exists below its target.
			target := filepath.Join(h.dir, "real", link)
			if err := os.Symlink(target, filepath.Join(h.dir, link)); err != nil {
				t.Fatal(err)
			}
			ds.set("s1", server.DeclaredArtifact{Name: "file", Path: "./a/b/file.md"})
			if c, err := h.read(h.refID(kindDeclared, "s1", "file"), 0, 0); err == nil {
				t.Fatalf("read through a symlinked %s: %q", link, c.Text)
			}
			if refs := h.svc.stageArtifacts("s1", state.StatusDone); len(refs) != 0 {
				t.Fatalf("listed through a symlinked %s: %+v", link, refs)
			}
		})
	}
}

// Swapping a parent directory for a symlink while the walk is under way cannot redirect the read: a swap before
// the component is opened is refused, one after it reads from the directory the descriptor holds.
func TestArtifactParentSwapCannotRedirect(t *testing.T) {
	for _, swapAfter := range []int{0, 1} {
		t.Run("after component "+string(rune('0'+swapAfter)), func(t *testing.T) {
			h, ds := declaredHarness(t, nil)
			h.writeFile("s1/sub/state.json", "stage placeholder")
			h.writeFile("state.json", "private run-root state")
			ds.set("s1", server.DeclaredArtifact{Name: "nested", Path: "./sub/state.json"})
			sub := filepath.Join(h.dir, "s1", "sub")
			swaps := 0
			lookupHook = func(rel string, i int) {
				if rel != "s1/sub/state.json" || i != swapAfter {
					return
				}
				if err := os.Rename(sub, sub+".saved"); err != nil {
					t.Error(err)
				}
				if err := os.Symlink("..", sub); err != nil {
					t.Error(err)
				}
				swaps++
			}
			defer func() { lookupHook = nil }()

			got, err := h.read(h.refID(kindDeclared, "s1", "nested"), 0, 0)
			if swaps != 1 {
				t.Fatalf("the swap ran %d times, want once: the walk did not reach component %d", swaps, swapAfter)
			}
			if err == nil && got.Text != "stage placeholder" {
				t.Fatalf("the read was redirected to %q", got.Text)
			}
			if swapAfter == 0 && err == nil {
				t.Fatal("a directory swapped for a symlink before its open was followed")
			}
			if swapAfter == 1 && (err != nil || got.Text != "stage placeholder") {
				t.Fatalf("the held directory did not serve its file: %+v, %v", got, err)
			}
		})
	}
}

// Every reader goes through the one descriptor walk: plan, summary, image, declared file, the approval snapshot
// and the plan resource.
func TestArtifactAllReadsUseDescriptorWalk(t *testing.T) {
	h, ds := declaredHarness(t, nil)
	awaitingPlan(h, "# plan")
	h.writeFile("s1/plan.md", "# done plan")
	h.writeFile("s1/execution_summary.md", "summary")
	h.writeFile("s1/artifacts/a.png", string(pngBytes(t, 2, 2)))
	h.writeFile("s1/out/report.md", "report")
	ds.set("s1", server.DeclaredArtifact{Name: "report", Path: "./out/report.md"})
	var mu sync.Mutex
	walked := map[string]bool{}
	lookupHook = func(rel string, _ int) {
		mu.Lock()
		walked[rel] = true
		mu.Unlock()
	}
	defer func() { lookupHook = nil }()

	for _, id := range []string{
		h.refID(kindPlan, "s1", ""), h.refID(kindSummary, "s1", ""), h.refID(kindImage, "s1", "a.png"), h.refID(kindDeclared, "s1", "report"),
	} {
		if _, err := h.read(id, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, rev, err := h.svc.reviewedPlan(context.Background(), "plan"); err != nil || rev != "rev-1" {
		t.Fatalf("reviewed plan: %q, %v", rev, err)
	}
	for _, rel := range []string{"s1/plan.md", "s1/execution_summary.md", "s1/artifacts/a.png", "s1/out/report.md", "plan/plan.md"} {
		if !walked[rel] {
			t.Errorf("%s was not read through the descriptor walk (walked %v)", rel, walked)
		}
	}
}

// The image listing enumerates its directory through the walk too, so a symlinked artifacts directory lists nothing.
func TestArtifactImageListingRefusesSymlinkedDirectory(t *testing.T) {
	h := newHarness(t, "")
	h.writeFile("other/artifacts/a.png", string(pngBytes(t, 2, 2)))
	if err := os.MkdirAll(filepath.Join(h.dir, "s1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(h.dir, "other", "artifacts"), filepath.Join(h.dir, "s1", "artifacts")); err != nil {
		t.Fatal(err)
	}
	if refs := h.svc.stageArtifacts("s1", state.StatusDone); len(refs) != 0 {
		t.Fatalf("refs = %+v", refs)
	}
	if _, err := h.read(h.refID(kindImage, "s1", "a.png"), 0, 0); err == nil {
		t.Fatal("image read through a symlinked artifacts directory")
	}
}
