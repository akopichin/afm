package pipeline

import (
	"errors"
	"fmt"
	"iter"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/secrets"
)

// LayerOrigin names who defined a configuration record.
type LayerOrigin string

const (
	OriginBase    LayerOrigin = "base" // global and project config.yaml
	OriginPackage LayerOrigin = "package"
	OriginOptions LayerOrigin = "options"
)

const filePrefix = "file:"

// commandNamePattern keeps a recipe name a plain file name: Docker writes a wrapper script under it.
var commandNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Layer is the strict subset of config.Config a package or an options file may define: agent recipes and
// helper profiles. Everything else (image, mounts, browser, ...) belongs to the user's environment.
type Layer struct {
	Origin   LayerOrigin
	Agents   map[string]config.AgentRecipe
	Profiles map[string]config.ProfileConfig
}

// Warning is a non-fatal observation about the assembled configuration.
type Warning string

// Provenance records which layer defined each effective record.
type Provenance struct {
	Agents   map[string]LayerOrigin
	Profiles map[string]LayerOrigin
}

// ParseLayer decodes a package agents file or an options file. baseDir is the snapshot directory for a package and
// the options file's directory for options: relative file: references are made absolute against it, once.
func ParseLayer(data []byte, origin LayerOrigin, baseDir string) (Layer, error) {
	layer := Layer{Origin: origin, Agents: map[string]config.AgentRecipe{}, Profiles: map[string]config.ProfileConfig{}}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Layer{}, fmt.Errorf("%s layer: %w", origin, err)
	}
	if len(doc.Content) == 0 {
		return layer, nil
	}
	root := deref(doc.Content[0])
	if root.Kind != yaml.MappingNode {
		return Layer{}, fmt.Errorf("%s layer: top level must be a mapping with docker.agents and/or collaboration.profiles", origin)
	}
	p := layerParser{layer: &layer, baseDir: baseDir}
	for key, val := range mappingPairs(root) {
		var err error
		switch key {
		case "docker":
			err = p.docker(val)
		case "collaboration":
			err = p.collaboration(val)
		default:
			err = p.notAllowed(key)
		}
		if err != nil {
			return Layer{}, err
		}
	}
	return layer, nil
}

type layerParser struct {
	layer   *Layer
	baseDir string
}

func (p layerParser) notAllowed(keyPath string) error {
	return fmt.Errorf("%s: key is not allowed in a pipeline %s layer; only docker.agents and collaboration.profiles are supported", keyPath, p.layer.Origin)
}

func (p layerParser) docker(n *yaml.Node) error {
	n = deref(n)
	if n.Kind != yaml.MappingNode {
		return errors.New("docker: must be a mapping with agents")
	}
	for key, val := range mappingPairs(n) {
		if key != "agents" {
			return p.notAllowed("docker." + key)
		}
		val = deref(val)
		if val.Kind != yaml.MappingNode {
			return errors.New("docker.agents: must be a mapping of command name to recipe")
		}
		for name, recipeNode := range mappingPairs(val) {
			if !commandNamePattern.MatchString(name) {
				return fmt.Errorf("docker.agents.%s: a command name may only contain letters, digits, '.', '_' and '-' (it becomes a wrapper file name)", name)
			}
			recipe, err := p.recipe("docker.agents."+name, recipeNode)
			if err != nil {
				return err
			}
			p.layer.Agents[name] = recipe
		}
	}
	return nil
}

func (p layerParser) recipe(at string, n *yaml.Node) (config.AgentRecipe, error) {
	n = deref(n)
	if n.Kind != yaml.MappingNode {
		return config.AgentRecipe{}, fmt.Errorf("%s: must be a mapping", at)
	}
	if err := checkKeys(at, n, reflect.TypeFor[config.AgentRecipe]()); err != nil {
		return config.AgentRecipe{}, err
	}
	for key, val := range mappingPairs(n) {
		if val = deref(val); key == "auth" && val.Kind == yaml.MappingNode {
			if err := checkKeys(at+".auth", val, reflect.TypeFor[config.RecipeAuth]()); err != nil {
				return config.AgentRecipe{}, err
			}
		}
	}
	var r config.AgentRecipe
	if err := n.Decode(&r); err != nil {
		return config.AgentRecipe{}, fmt.Errorf("%s: %w", at, err)
	}
	if err := r.Validate(); err != nil {
		return config.AgentRecipe{}, fmt.Errorf("%s: %w", at, err)
	}
	return p.pinPaths(at, r)
}

// pinPaths applies the layer's file-reference rules. A package may only name env: secrets and prompt files inside
// its snapshot; options keep the user's full semantics but get absolute paths.
func (p layerParser) pinPaths(at string, r config.AgentRecipe) (config.AgentRecipe, error) {
	if ref, ok := strings.CutPrefix(r.SystemPrompt, filePrefix); ok {
		abs, err := p.filePath(at+".system_prompt", ref)
		if err != nil {
			return r, err
		}
		r.SystemPrompt = filePrefix + abs
	}
	if p.layer.Origin == OriginPackage && r.Auth.From != "" && !strings.HasPrefix(r.Auth.From, "env:") {
		return r, fmt.Errorf("%s.auth.from: a package may only reference env: secrets", at)
	}
	if ref, ok := strings.CutPrefix(r.Auth.From, filePrefix); ok && !strings.HasPrefix(ref, "~") {
		abs, err := p.filePath(at+".auth.from", ref)
		if err != nil {
			return r, err
		}
		r.Auth.From = filePrefix + abs
	}
	return r, nil
}

// filePath turns a file: reference into an absolute path without opening it: a named pipe or device named by a
// hostile package must never be read during loading.
func (p layerParser) filePath(at, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("%s: file: reference has no path", at)
	}
	if p.layer.Origin == OriginPackage {
		if err := checkPackagePath(at, ref); err != nil {
			return "", err
		}
		abs := filepath.Join(p.baseDir, filepath.FromSlash(ref))
		if info, err := os.Lstat(abs); err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s: %q is not a regular file in the package", at, ref)
		}
		return abs, nil
	}
	home, _ := os.UserHomeDir()
	ref = secrets.ExpandHome(ref, home)
	if filepath.IsAbs(ref) {
		return filepath.Clean(ref), nil
	}
	return filepath.Join(p.baseDir, ref), nil
}

func (p layerParser) collaboration(n *yaml.Node) error {
	n = deref(n)
	if n.Kind != yaml.MappingNode {
		return errors.New("collaboration: must be a mapping with profiles")
	}
	for key, val := range mappingPairs(n) {
		if key != "profiles" {
			return p.notAllowed("collaboration." + key)
		}
		val = deref(val)
		if val.Kind != yaml.MappingNode {
			return errors.New("collaboration.profiles: must be a mapping of profile id to profile")
		}
		for id, profileNode := range mappingPairs(val) {
			at := "collaboration.profiles." + id
			profileNode = deref(profileNode)
			if profileNode.Kind != yaml.MappingNode {
				return fmt.Errorf("%s: must be a mapping", at)
			}
			if err := checkKeys(at, profileNode, reflect.TypeFor[config.ProfileConfig]()); err != nil {
				return err
			}
			var profile config.ProfileConfig
			if err := profileNode.Decode(&profile); err != nil {
				return fmt.Errorf("%s: %w", at, err)
			}
			p.layer.Profiles[id] = profile
		}
	}
	return nil
}

type layerDocker struct {
	Agents map[string]config.AgentRecipe `yaml:"agents"`
}

type layerCollaboration struct {
	Profiles map[string]config.ProfileConfig `yaml:"profiles"`
}

type layerDoc struct {
	Docker        *layerDocker        `yaml:"docker,omitempty"`
	Collaboration *layerCollaboration `yaml:"collaboration,omitempty"`
}

// EncodeLayer renders a layer as YAML that ParseLayer reads back, so a container rebuilds the same options layer
// from the launch record without access to the options file. Only selectors are written, never secret values.
func EncodeLayer(l Layer) ([]byte, error) {
	var doc layerDoc
	if len(l.Agents) > 0 {
		doc.Docker = &layerDocker{Agents: l.Agents}
	}
	if len(l.Profiles) > 0 {
		doc.Collaboration = &layerCollaboration{Profiles: l.Profiles}
	}
	return yaml.Marshal(doc)
}

// ValidatePackage checks what only a package must satisfy: names carry the package prefix, and every manifest role
// points at a record the package defines.
func (l Layer) ValidatePackage(m Manifest) error {
	prefix := m.Name + "-"
	for _, name := range slices.Sorted(maps.Keys(l.Agents)) {
		if !strings.HasPrefix(name, prefix) {
			return fmt.Errorf("docker.agents.%s: package command names must start with %q", name, prefix)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(l.Profiles)) {
		if !strings.HasPrefix(id, prefix) {
			return fmt.Errorf("collaboration.profiles.%s: package profile ids must start with %q", id, prefix)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(m.Roles)) {
		role := m.Roles[id]
		if _, ok := l.Agents[role.Command]; !ok {
			return fmt.Errorf("roles.%s.command: %q is not defined in the package docker.agents", id, role.Command)
		}
		if _, ok := l.Profiles[role.Profile]; role.Profile != "" && !ok {
			return fmt.Errorf("roles.%s.profile: %q is not defined in the package collaboration.profiles", id, role.Profile)
		}
	}
	return nil
}

// Assemble builds the effective config: base, then the package layer, then options; a record is replaced as a whole by
// key. base is not modified. The snapshot is added to docker.extra_mounts so a container sees it at the same path.
func Assemble(base config.Config, pkg Layer, opts *Layer, snapshotDir string) (config.Config, Provenance, []Warning, error) {
	cfg := base
	cfg.Docker.Agents = maps.Clone(base.Docker.Agents)
	cfg.Collaboration.Profiles = maps.Clone(base.Collaboration.Profiles)
	cfg.Docker.ExtraMounts = slices.Clone(base.Docker.ExtraMounts)
	prov := Provenance{Agents: map[string]LayerOrigin{}, Profiles: map[string]LayerOrigin{}}
	for name := range cfg.Docker.Agents {
		prov.Agents[name] = OriginBase
	}
	for id := range cfg.Collaboration.Profiles {
		prov.Profiles[id] = OriginBase
	}
	var warnings []Warning

	for _, name := range slices.Sorted(maps.Keys(pkg.Agents)) {
		if prov.Agents[name] == OriginBase {
			warnings = append(warnings, Warning(fmt.Sprintf("package recipe docker.agents.%s shadows a recipe from the afm config", name)))
		}
		if r := pkg.Agents[name]; r.URL != "" && r.Auth.From != "" {
			warnings = append(warnings, Warning(fmt.Sprintf("package recipe docker.agents.%s sends the secret %s to %s; check that you trust that endpoint", name, r.Auth.From, r.URL)))
		}
		setAgent(&cfg, prov, name, pkg.Agents[name], OriginPackage)
	}
	for _, id := range slices.Sorted(maps.Keys(pkg.Profiles)) {
		if prov.Profiles[id] == OriginBase {
			warnings = append(warnings, Warning(fmt.Sprintf("package profile collaboration.profiles.%s shadows a profile from the afm config", id)))
		}
		setProfile(&cfg, prov, id, pkg.Profiles[id], OriginPackage)
	}
	if opts != nil {
		for _, name := range slices.Sorted(maps.Keys(opts.Agents)) {
			setAgent(&cfg, prov, name, opts.Agents[name], OriginOptions)
		}
		for _, id := range slices.Sorted(maps.Keys(opts.Profiles)) {
			profile := opts.Profiles[id]
			if prov.Profiles[id] == OriginPackage && profile.Command == "" {
				warnings = append(warnings, Warning(fmt.Sprintf("options profile collaboration.profiles.%s replaces the package profile entirely and sets no command", id)))
			}
			setProfile(&cfg, prov, id, profile, OriginOptions)
		}
	}
	clean := filepath.Clean(snapshotDir)
	if !slices.ContainsFunc(cfg.Docker.ExtraMounts, func(m config.ExtraMount) bool { return filepath.Clean(m.Path) == clean }) {
		cfg.Docker.ExtraMounts = append(cfg.Docker.ExtraMounts, config.ExtraMount{Path: snapshotDir})
	}
	if err := cfg.Docker.ExtraMounts.Validate(); err != nil {
		return base, Provenance{}, nil, fmt.Errorf("docker.extra_mounts: %w", err)
	}
	return cfg, prov, warnings, nil
}

func setAgent(cfg *config.Config, prov Provenance, name string, r config.AgentRecipe, origin LayerOrigin) {
	if cfg.Docker.Agents == nil {
		cfg.Docker.Agents = map[string]config.AgentRecipe{}
	}
	cfg.Docker.Agents[name] = r
	prov.Agents[name] = origin
}

func setProfile(cfg *config.Config, prov Provenance, id string, p config.ProfileConfig, origin LayerOrigin) {
	if cfg.Collaboration.Profiles == nil {
		cfg.Collaboration.Profiles = map[string]config.ProfileConfig{}
	}
	cfg.Collaboration.Profiles[id] = p
	prov.Profiles[id] = origin
}

// checkKeys rejects mapping keys that are not yaml fields of typ, naming the full key path.
func checkKeys(at string, n *yaml.Node, typ reflect.Type) error {
	known := map[string]bool{}
	for i := range typ.NumField() {
		if name, _, _ := strings.Cut(typ.Field(i).Tag.Get("yaml"), ","); name != "" && name != "-" {
			known[name] = true
		}
	}
	for key := range mappingPairs(n) {
		if !known[key] {
			return fmt.Errorf("%s.%s: unknown key (allowed: %s)", at, key, strings.Join(slices.Sorted(maps.Keys(known)), ", "))
		}
	}
	return nil
}

func deref(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

// mappingPairs iterates a mapping node's keys with their values in document order.
func mappingPairs(n *yaml.Node) iter.Seq2[string, *yaml.Node] {
	return func(yield func(string, *yaml.Node) bool) {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if !yield(n.Content[i].Value, n.Content[i+1]) {
				return
			}
		}
	}
}
