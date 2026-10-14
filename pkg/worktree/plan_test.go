package worktree

import (
	"strings"
	"testing"
)

func TestPlanFor_Valid(t *testing.T) {
	p, err := PlanFor("/proj", "dev-pipeline")
	if err != nil {
		t.Fatalf("PlanFor returned error: %v", err)
	}
	if p.Base != "/proj" {
		t.Errorf("Base = %q, want %q", p.Base, "/proj")
	}
	if p.Dir != "/proj/.worktrees" {
		t.Errorf("Dir = %q, want %q", p.Dir, "/proj/.worktrees")
	}
	if p.Path != "/proj/.worktrees/dev-pipeline" {
		t.Errorf("Path = %q, want %q", p.Path, "/proj/.worktrees/dev-pipeline")
	}
	if p.Branch != "dev-pipeline" {
		t.Errorf("Branch = %q, want %q", p.Branch, "dev-pipeline")
	}
}

func TestPlanFor_RejectsUnsafeNames(t *testing.T) {
	names := []string{
		"",
		".",
		"..",
		"a/b",
		"a\\b",
		"-lead",
		".lead",
		"trail.lock",
		"has space",
		"has~tilde",
		"has^caret",
		"has:colon",
		"has?q",
		"has*star",
		"has[br]",
		"a..b",
		"end.",
		"end/",
		"with\x00nul",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			_, err := PlanFor("/proj", name)
			if err == nil {
				t.Fatalf("PlanFor(%q) succeeded, want error", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not mention flow name %q", err.Error(), name)
			}
		})
	}
}

func TestPlanFor_AcceptsRealisticNames(t *testing.T) {
	names := []string{"dev-pipeline", "feature_x", "v1.2", "AFM-123"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			if _, err := PlanFor("/proj", name); err != nil {
				t.Errorf("PlanFor(%q) returned error: %v", name, err)
			}
		})
	}
}

func TestValidateName_RejectsUnsafeNames(t *testing.T) {
	names := []string{"", ".", "..", "a/b", "-lead", ".lead", "trail.lock", "has space", ".."}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			err := ValidateName(name)
			if err == nil {
				t.Fatalf("ValidateName(%q) succeeded, want error", name)
			}
			if !strings.Contains(err.Error(), "worktree name") {
				t.Errorf("error %q does not mention 'worktree name'", err.Error())
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not mention rejected name %q verbatim", err.Error(), name)
			}
		})
	}
}

func TestValidateName_AcceptsRealisticNames(t *testing.T) {
	names := []string{"dev-pipeline", "feature_x", "v1.2", "AFM-123"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			if err := ValidateName(name); err != nil {
				t.Errorf("ValidateName(%q) returned error: %v", name, err)
			}
		})
	}
}
