package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/fsutil"
)

const bindingExt = ".json"

// PackageRef names the package version a run was started with.
type PackageRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// OptionsRef identifies the options file a run was last started with, never its contents.
type OptionsRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// RecipeRecord is the executor identity of one command at the last start: the recipe type and a digest of its
// selectors. Secret values never enter it.
type RecipeRecord struct {
	Type     string `json:"type"`
	Identity string `json:"identity"`
}

// Binding ties a run to the exact package snapshot it uses. It lives outside the run directory so a later
// `afm run --pipeline` can find the version to continue on.
type Binding struct {
	RunID         string                  `json:"run_id"`
	RunName       string                  `json:"run_name"`
	Worktree      string                  `json:"worktree"` // the --worktree value; empty when the flag was not given
	Package       PackageRef              `json:"package"`
	Source        Source                  `json:"source"`
	Commit        string                  `json:"commit,omitempty"`
	ContentSHA256 string                  `json:"content_sha256"`
	SnapshotDir   string                  `json:"snapshot_dir"`
	Options       *OptionsRef             `json:"options,omitempty"`
	Recipes       map[string]RecipeRecord `json:"recipes,omitempty"`
	CreatedAt     time.Time               `json:"created_at"`
	UpdatedAt     time.Time               `json:"updated_at"`
}

// BindingsDir is where the bindings of the runs under afmRoot live.
func BindingsDir(afmRoot string) string {
	return filepath.Join(afmRoot, config.AfmDir, "pipelines", "runs")
}

func bindingPath(dir, runID string) (string, error) {
	if runID == "" || runID == "." || runID == parentDir || filepath.Base(runID) != runID || strings.ContainsAny(runID, `/\`) {
		return "", fmt.Errorf("run id %q is not a valid binding name", runID)
	}
	return filepath.Join(dir, runID+bindingExt), nil
}

// WriteBinding atomically stores b under its run id.
func WriteBinding(dir string, b Binding) error {
	path, err := bindingPath(dir, b.RunID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("encode binding: %w", err)
	}
	return fsutil.AtomicWriteFile(path, append(data, '\n'), 0o644)
}

// ReadBinding returns the binding of one run, or false when it has none.
func ReadBinding(dir, runID string) (Binding, bool, error) {
	path, err := bindingPath(dir, runID)
	if err != nil {
		return Binding{}, false, err
	}
	b, err := readBinding(path)
	if errors.Is(err, os.ErrNotExist) {
		return Binding{}, false, nil
	}
	return b, err == nil, err
}

// DeleteBinding removes the binding of a run; a missing one is not an error.
func DeleteBinding(dir, runID string) error {
	path, err := bindingPath(dir, runID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove binding: %w", err)
	}
	return nil
}

// ReadBindings returns every readable binding in dir. A damaged file does not hide the others: it is reported in the
// returned error while the good bindings are still returned. A missing directory is empty.
func ReadBindings(dir string) ([]Binding, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read bindings: %w", err)
	}
	var bindings []Binding
	var problems []error
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != bindingExt {
			continue
		}
		b, err := readBinding(filepath.Join(dir, e.Name()))
		if err != nil {
			problems = append(problems, fmt.Errorf("binding %s: %w", e.Name(), err))
			continue
		}
		bindings = append(bindings, b)
	}
	return bindings, errors.Join(problems...)
}

func readBinding(path string) (Binding, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Binding{}, err
	}
	var b Binding
	if err := json.Unmarshal(data, &b); err != nil {
		return Binding{}, err
	}
	if b.RunID+bindingExt != filepath.Base(path) {
		return Binding{}, fmt.Errorf("run_id %q does not match the file name", b.RunID)
	}
	return b, nil
}

// FindBindings selects the bindings of one source identity started with one --worktree value, newest update first.
func FindBindings(bindings []Binding, identity, worktree string) []Binding {
	var found []Binding
	for _, b := range bindings {
		if b.Source.Identity() == identity && b.Worktree == worktree {
			found = append(found, b)
		}
	}
	slices.SortFunc(found, func(a, b Binding) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.RunID, a.RunID)
	})
	return found
}
