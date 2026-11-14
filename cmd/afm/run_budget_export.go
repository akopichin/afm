package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/flow"
)

// Fixed refusal texts: they reach the dashboard and the SDK, so they never carry OS errors or absolute paths.
const (
	reasonSymlink       = "flow file is a symbolic link"
	reasonNotWritable   = "flow file or its directory is not writable"
	reasonUnreadable    = "flow file cannot be read"
	reasonInvalidYAML   = "flow file is not valid YAML"
	reasonChanged       = "the declaration changed outside this run"
	reasonNotFound      = "the flow file does not declare this scope"
	reasonAmbiguous     = "the declaration is not unique in the flow file"
	reasonAnchor        = "the declaration uses anchors, aliases or merge keys"
	reasonInline        = "the declaration is written inline"
	reasonNameOnly      = "the helper is declared by profile name only"
	reasonBlockShape    = "the budget block cannot be rewritten safely"
	reasonBlockComments = "the budget block contains comments"
	reasonCheckFailed   = "the rewritten flow file did not pass the check"
	reasonWriteFailed   = "the flow file could not be written"
	reasonBadSpec       = "the limit cannot be written as YAML"
)

// budgetExporter saves a budget override into the flow file this run started from. It edits the text of the
// budget block only, because re-encoding the YAML tree would drop blank lines and indentation.
type budgetExporter struct {
	mu       sync.Mutex // serializes this process's exports; the byte recheck guards against other writers
	path     string
	shown    string // path relative to afm-root, the only form clients see
	flow     *flow.Flow
	exported map[budget.DeclarationRef]*budget.Spec // budgets this run wrote, newer than flow
}

func newBudgetExporter(flowPath, afmRoot string, f *flow.Flow) *budgetExporter {
	return &budgetExporter{path: flowPath, shown: relativeToRoot(afmRoot, flowPath), flow: f, exported: map[budget.DeclarationRef]*budget.Spec{}}
}

// relativeToRoot hides absolute paths; a file outside the root is shown by its name.
func relativeToRoot(root, path string) string {
	abs, err := filepath.Abs(path)
	if err == nil {
		if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(path)
}

// Check reports, without touching the file, whether ref can be saved. reason is empty when it can.
func (e *budgetExporter) Check(ref budget.DeclarationRef) (file, reason string, declared *budget.Spec) {
	e.mu.Lock()
	defer e.mu.Unlock()
	data, _, reason := e.readFile()
	if reason != "" {
		return e.shown, reason, nil
	}
	current, root, reason := decodeFlowYAML(data)
	if reason != "" {
		return e.shown, reason, nil
	}
	declared, reason = declaredBudget(current, ref)
	if reason != "" {
		return e.shown, reason, nil
	}
	target, reason := locateDeclaration(root, ref)
	if reason == "" {
		// A dry run of the edit: the same refusals the export itself would give.
		_, reason = rewriteBudgetBlock(data, target, []string{"tokens: 1"})
	}
	return e.shown, reason, declared
}

// Export writes spec into the declaration. A refusal is a result, not an error.
func (e *budgetExporter) Export(_ context.Context, ref budget.DeclarationRef, spec budget.Spec) budget.ExportResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	out, status, reason := e.save(ref, spec)
	if reason != "" {
		return budget.ExportResult{Status: status, Path: e.shown, Error: reason}
	}
	e.exported[ref] = &out
	return budget.ExportResult{Status: budget.ExportSaved, Path: e.shown}
}

func (e *budgetExporter) save(ref budget.DeclarationRef, spec budget.Spec) (budget.Spec, budget.ExportStatus, string) {
	spec = spec.Normalize()
	block, err := renderBudgetSpec(spec)
	if err != nil {
		return spec, budget.ExportFailed, reasonBadSpec
	}
	data, read, reason := e.readFile()
	if reason != "" {
		return spec, budget.ExportFailed, reason
	}
	current, root, reason := decodeFlowYAML(data)
	if reason != "" {
		return spec, budget.ExportFailed, reason
	}
	declared, reason := declaredBudget(current, ref)
	if reason != "" {
		return spec, budget.ExportUnavailable, reason
	}
	if !sameBudget(declared, e.knownBudget(ref)) {
		return spec, budget.ExportFailed, reasonChanged
	}
	target, reason := locateDeclaration(root, ref)
	if reason != "" {
		return spec, budget.ExportUnavailable, reason
	}
	rewritten, reason := rewriteBudgetBlock(data, target, block)
	if reason != "" {
		return spec, budget.ExportUnavailable, reason
	}
	if !rewriteKeepsFlow(data, rewritten, ref, spec) {
		return spec, budget.ExportFailed, reasonCheckFailed
	}
	if reason := e.replaceFile(data, rewritten, read); reason != "" {
		return spec, budget.ExportFailed, reason
	}
	return spec, "", ""
}

// knownBudget is what this run last knew the file to declare for ref.
func (e *budgetExporter) knownBudget(ref budget.DeclarationRef) *budget.Spec {
	if spec, ok := e.exported[ref]; ok {
		return spec
	}
	spec, _ := declaredBudget(e.flow, ref)
	return spec
}

func sameBudget(a, b *budget.Spec) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// readFile returns the flow bytes and the file's identity after the path checks.
func (e *budgetExporter) readFile() ([]byte, os.FileInfo, string) {
	info, reason := e.checkPath()
	if reason != "" {
		return nil, nil, reason
	}
	data, err := os.ReadFile(e.path)
	if err != nil {
		return nil, nil, reasonUnreadable
	}
	return data, info, ""
}

// checkPath refuses a flow file that is a symlink or that cannot be replaced.
func (e *budgetExporter) checkPath() (os.FileInfo, string) {
	info, err := os.Lstat(e.path)
	if err != nil {
		return nil, reasonUnreadable
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, reasonSymlink
	}
	if !writable(e.path, info) || !writableDir(filepath.Dir(e.path)) {
		return nil, reasonNotWritable
	}
	return info, ""
}

// replaceFile swaps the file through a temp file in the same directory, so a failure leaves the original.
// read is the file as it was when original was read; the swap is refused if the path no longer is that file.
func (e *budgetExporter) replaceFile(original, rewritten []byte, read os.FileInfo) string {
	tmp, err := os.CreateTemp(filepath.Dir(e.path), ".afm-flow-*.tmp")
	if err != nil {
		return reasonWriteFailed
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // no-op after a successful rename
	if _, err := tmp.Write(rewritten); err != nil {
		_ = tmp.Close()
		return reasonWriteFailed
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return reasonWriteFailed
	}
	if err := tmp.Close(); err != nil {
		return reasonWriteFailed
	}
	// The result must be a flow the parser accepts, or the run would fail to load it next time.
	if _, err := flow.ParseFile(tmp.Name()); err != nil {
		return reasonCheckFailed
	}
	// The last look before the swap: the path may have become a symlink or another file, or lost write access,
	// and someone else may have written the file since it was read.
	now, reason := e.checkPath()
	if reason != "" {
		return reason
	}
	if !os.SameFile(read, now) {
		return reasonChanged
	}
	if data, err := os.ReadFile(e.path); err != nil || !bytes.Equal(data, original) {
		return reasonChanged
	}
	if err := os.Chmod(tmp.Name(), now.Mode().Perm()); err != nil {
		return reasonWriteFailed
	}
	if err := os.Rename(tmp.Name(), e.path); err != nil {
		return reasonWriteFailed
	}
	return ""
}

// renderBudgetSpec renders the spec as the lines of a budget mapping, one key per line.
func renderBudgetSpec(spec budget.Spec) ([]string, error) {
	value, err := spec.MarshalYAML()
	if err != nil {
		return nil, err
	}
	text, err := yaml.Marshal(value)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(text), "\n"), "\n"), nil
}

// decodeFlowYAML decodes with the flow parser's own YAML hooks but without its validation and warnings.
func decodeFlowYAML(data []byte) (*flow.Flow, *yaml.Node, string) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil || len(root.Content) == 0 {
		return nil, nil, reasonInvalidYAML
	}
	var f flow.Flow
	if err := root.Decode(&f); err != nil {
		return nil, nil, reasonInvalidYAML
	}
	return &f, root.Content[0], ""
}

// declaredBudget is the budget the flow declares for ref; the reason is set when ref names no single declaration.
func declaredBudget(f *flow.Flow, ref budget.DeclarationRef) (*budget.Spec, string) {
	slot, reason := budgetSlot(f, ref)
	if reason != "" {
		return nil, reason
	}
	return *slot, ""
}

// budgetSlot points at the Budget field of the declaration.
func budgetSlot(f *flow.Flow, ref budget.DeclarationRef) (**budget.Spec, string) {
	switch ref.Kind {
	case budget.DeclFlow:
		return &f.Budget, ""
	case budget.DeclStage, budget.DeclStageAssistant:
		var stage *flow.Stage
		for i := range f.Stages {
			if f.Stages[i].ID != ref.StageID {
				continue
			}
			if stage != nil {
				return nil, reasonAmbiguous
			}
			stage = &f.Stages[i]
		}
		if stage == nil {
			return nil, reasonNotFound
		}
		if ref.Kind == budget.DeclStage {
			return &stage.Budget, ""
		}
		var helper *flow.StageAssistant
		for i := range stage.Assistants {
			if stage.Assistants[i].Profile != ref.Profile {
				continue
			}
			if helper != nil {
				return nil, reasonAmbiguous
			}
			helper = &stage.Assistants[i]
		}
		if helper == nil {
			return nil, reasonNotFound
		}
		return &helper.Budget, ""
	case budget.DeclTeamMember:
		var member *flow.TeamMember
		for i := range f.Team {
			if f.Team[i].ID != ref.MemberID {
				continue
			}
			if member != nil {
				return nil, reasonAmbiguous
			}
			member = &f.Team[i]
		}
		if member == nil {
			return nil, reasonNotFound
		}
		return &member.Budget, ""
	}
	return nil, reasonNotFound
}

// rewriteKeepsFlow checks that the new bytes declare spec for ref and that nothing else in the flow moved.
func rewriteKeepsFlow(before, after []byte, ref budget.DeclarationRef, spec budget.Spec) bool {
	want, _, reason := decodeFlowYAML(before)
	if reason != "" {
		return false
	}
	slot, reason := budgetSlot(want, ref)
	if reason != "" {
		return false
	}
	*slot = &spec
	got, _, reason := decodeFlowYAML(after)
	return reason == "" && reflect.DeepEqual(want, got)
}

// declarationNode is where the budget block goes: the declaration mapping and the key that identifies it.
type declarationNode struct {
	mapping *yaml.Node
	idKey   *yaml.Node // nil for the flow root
}

// locateDeclaration finds the declaration's mapping in the YAML tree and refuses any form the text edit
// cannot reproduce faithfully.
func locateDeclaration(root *yaml.Node, ref budget.DeclarationRef) (declarationNode, string) {
	if reason := plainMapping(root); reason != "" {
		return declarationNode{}, reason
	}
	switch ref.Kind {
	case budget.DeclFlow:
		return declarationNode{mapping: root}, ""
	case budget.DeclTeamMember:
		item, reason := findByKey(root, "team", "id", ref.MemberID)
		return identified(item, "id", reason)
	case budget.DeclStage:
		item, reason := findByKey(root, "stages", "id", ref.StageID)
		return identified(item, "id", reason)
	case budget.DeclStageAssistant:
		stage, reason := findByKey(root, "stages", "id", ref.StageID)
		if reason != "" {
			return declarationNode{}, reason
		}
		item, reason := findAssistant(stage, ref.Profile)
		return identified(item, "profile", reason)
	}
	return declarationNode{}, reasonNotFound
}

func identified(mapping *yaml.Node, idKey, reason string) (declarationNode, string) {
	if reason != "" {
		return declarationNode{}, reason
	}
	_, key, value := lookupKey(mapping, idKey)
	if key == nil || value.Kind != yaml.ScalarNode || value.Line != key.Line || value.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return declarationNode{}, reasonBlockShape
	}
	return declarationNode{mapping: mapping, idKey: key}, ""
}

// plainMapping refuses what a text edit cannot place: aliases, anchors, merge keys and inline mappings.
func plainMapping(n *yaml.Node) string {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return reasonAnchor
	}
	if n.Kind != yaml.MappingNode {
		return reasonBlockShape
	}
	if n.Style&yaml.FlowStyle != 0 {
		return reasonInline
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i]
		if key.Kind == yaml.AliasNode || key.Anchor != "" || key.Tag == "!!merge" || key.Value == "<<" {
			return reasonAnchor
		}
	}
	return ""
}

// lookupKey returns the value of key in a mapping and how many times it occurs.
func lookupKey(m *yaml.Node, name string) (int, *yaml.Node, *yaml.Node) {
	count := 0
	var key, value *yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			count++
			key, value = m.Content[i], m.Content[i+1]
		}
	}
	return count, key, value
}

// findByKey finds the one item of the list under listKey whose idKey scalar equals id.
func findByKey(root *yaml.Node, listKey, idKey, id string) (*yaml.Node, string) {
	_, _, list := lookupKey(root, listKey)
	if list == nil {
		return nil, reasonNotFound
	}
	if list.Kind == yaml.AliasNode || list.Anchor != "" {
		return nil, reasonAnchor
	}
	if list.Kind != yaml.SequenceNode {
		return nil, reasonBlockShape
	}
	var found *yaml.Node
	for _, item := range list.Content {
		if item.Kind == yaml.AliasNode {
			return nil, reasonAnchor
		}
		if item.Kind != yaml.MappingNode {
			continue
		}
		if _, _, v := lookupKey(item, idKey); v != nil && v.Value == id {
			if found != nil {
				return nil, reasonAmbiguous
			}
			found = item
		}
	}
	if found == nil {
		return nil, reasonNotFound
	}
	if reason := plainMapping(found); reason != "" {
		return nil, reason
	}
	return found, ""
}

// findAssistant finds the one helper mapping of the stage with the profile; a name-only helper has no place
// for a budget.
func findAssistant(stage *yaml.Node, profile string) (*yaml.Node, string) {
	_, _, node := lookupKey(stage, "assistants")
	if node == nil {
		return nil, reasonNotFound
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return nil, reasonAnchor
	}
	items := []*yaml.Node{node}
	if node.Kind == yaml.SequenceNode {
		items = node.Content
	}
	var found *yaml.Node
	matches := 0
	for _, item := range items {
		switch item.Kind {
		case yaml.AliasNode:
			return nil, reasonAnchor
		case yaml.ScalarNode:
			if item.Value == profile {
				return nil, reasonNameOnly
			}
		case yaml.MappingNode:
			if _, _, v := lookupKey(item, "profile"); v != nil && v.Value == profile {
				matches++
				found = item
			}
		default:
			// Other node kinds cannot declare a helper; the flow parser rejects them.
		}
	}
	switch {
	case matches == 0:
		return nil, reasonNotFound
	case matches > 1:
		return nil, reasonAmbiguous
	}
	if reason := plainMapping(found); reason != "" {
		return nil, reason
	}
	return found, ""
}

// fileLine is one line of the flow file with its own terminator, so untouched lines keep their bytes.
type fileLine struct{ body, term string }

func splitFileLines(data []byte) []fileLine {
	var lines []fileLine
	for _, raw := range strings.SplitAfter(string(data), "\n") {
		if raw == "" {
			continue
		}
		body, term := raw, ""
		switch {
		case strings.HasSuffix(raw, "\r\n"):
			body, term = raw[:len(raw)-2], "\r\n"
		case strings.HasSuffix(raw, "\n"):
			body, term = raw[:len(raw)-1], "\n"
		default:
			// The last line of a file without a final newline has no terminator.
		}
		lines = append(lines, fileLine{body, term})
	}
	return lines
}

func joinFileLines(lines []fileLine) []byte {
	var out strings.Builder
	for _, l := range lines {
		out.WriteString(l.body)
		out.WriteString(l.term)
	}
	return []byte(out.String())
}

func lineEnding(lines []fileLine) string {
	for _, l := range lines {
		if l.term != "" {
			return l.term
		}
	}
	return "\n"
}

// rewriteBudgetBlock puts the rendered budget mapping into the declaration, replacing an existing budget block.
func rewriteBudgetBlock(data []byte, target declarationNode, block []string) ([]byte, string) {
	lines := splitFileLines(data)
	eol := lineEnding(lines)
	_, key, value := lookupKey(target.mapping, "budget")
	switch {
	case key == nil && target.idKey == nil:
		return joinFileLines(insertLines(lines, len(lines), newBudgetBlock(block, 0), eol)), ""
	case key == nil:
		return joinFileLines(insertLines(lines, target.idKey.Line, newBudgetBlock(block, target.idKey.Column-1), eol)), ""
	}
	if value.Kind == yaml.AliasNode || value.Anchor != "" {
		return nil, reasonAnchor
	}
	switch {
	case value.Kind == yaml.MappingNode && value.Style&yaml.FlowStyle == 0:
		return replaceBlockMapping(lines, key, value, block, eol)
	case value.Kind == yaml.MappingNode:
		return replaceInlineMapping(lines, key, value, block, eol)
	}
	return nil, reasonBlockShape
}

// newBudgetBlock is the `budget:` key at indent with the mapping lines one level deeper.
func newBudgetBlock(block []string, indent int) []string {
	return append([]string{strings.Repeat(" ", indent) + "budget:"}, indented(block, indent+2)...)
}

func indented(block []string, indent int) []string {
	pad := strings.Repeat(" ", indent)
	out := make([]string, len(block))
	for i, l := range block {
		out[i] = pad + l
	}
	return out
}

// insertLines adds bodies after the 1-based line `after`; a file without a final newline stays without one.
func insertLines(lines []fileLine, after int, bodies []string, eol string) []fileLine {
	added := make([]fileLine, len(bodies))
	for i, b := range bodies {
		added[i] = fileLine{b, eol}
	}
	out := append([]fileLine{}, lines[:after]...)
	if after > 0 && lines[after-1].term == "" {
		out[after-1].term = eol
		added[len(added)-1].term = ""
	}
	out = append(out, added...)
	return append(out, lines[after:]...)
}

// replaceBlockMapping swaps the child lines of `budget:` for the new ones, keeping the key line and its
// comment. Only a mapping of one-line scalars without comments is rewritten.
func replaceBlockMapping(lines []fileLine, key, value *yaml.Node, block []string, eol string) ([]byte, string) {
	if len(value.Content) == 0 || value.Content[0].Line <= key.Line {
		return nil, reasonBlockShape
	}
	first, last := value.Content[0].Line, value.Content[len(value.Content)-2].Line
	childIndent := value.Content[0].Column - 1
	for i := 0; i+1 < len(value.Content); i += 2 {
		k, v := value.Content[i], value.Content[i+1]
		if v.Kind != yaml.ScalarNode || v.Anchor != "" || v.Line != k.Line || v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 || k.Column-1 != childIndent {
			return nil, reasonBlockShape
		}
	}
	if last > len(lines) {
		return nil, reasonBlockShape
	}
	for _, l := range lines[first-1 : last] {
		if strings.HasPrefix(strings.TrimSpace(l.body), "#") || strings.Contains(l.body, " #") || strings.Contains(l.body, "\t#") {
			return nil, reasonBlockComments
		}
	}
	// A deeper line right after the block would be a continuation of its last value.
	if last < len(lines) {
		next := lines[last].body
		if trimmed := strings.TrimSpace(next); trimmed != "" && !strings.HasPrefix(trimmed, "#") && len(next)-len(strings.TrimLeft(next, " ")) > childIndent {
			return nil, reasonBlockShape
		}
	}
	out := append([]fileLine{}, lines[:first-1]...)
	tail := lines[last:]
	replaced := make([]fileLine, len(block))
	for i, b := range indented(block, childIndent) {
		replaced[i] = fileLine{b, eol}
	}
	if len(tail) == 0 {
		replaced[len(replaced)-1].term = lines[last-1].term // the file's last line keeps its ending
	}
	out = append(out, replaced...)
	return joinFileLines(append(out, tail...)), ""
}

// replaceInlineMapping turns `budget: {…}` on one line into a block under the same key, keeping what follows
// the braces (a comment).
func replaceInlineMapping(lines []fileLine, key, value *yaml.Node, block []string, eol string) ([]byte, string) {
	if value.Line != key.Line || key.Line > len(lines) {
		return nil, reasonBlockShape
	}
	runes := []rune(lines[key.Line-1].body)
	start := value.Column - 1
	end := closingBrace(runes, start)
	if end < 0 {
		return nil, reasonBlockShape
	}
	head := strings.TrimRight(string(runes[:start]), " ") + string(runes[end+1:])
	lines = append([]fileLine{}, lines...)
	lines[key.Line-1].body = head
	return joinFileLines(insertLines(lines, key.Line, indented(block, key.Column-1+2), eol)), ""
}

// closingBrace is the index of the brace closing the flow mapping that opens at runes[start], or -1.
func closingBrace(runes []rune, start int) int {
	if start >= len(runes) || runes[start] != '{' {
		return -1
	}
	depth := 0
	var quote rune
	for i := start; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '{':
			depth++
		case r == '}':
			depth--
			if depth == 0 {
				return i
			}
		default:
			// Any other character inside the braces belongs to a value.
		}
	}
	return -1
}
