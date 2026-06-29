package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathInside(t *testing.T) {
	tmp := t.TempDir()
	stageDir := filepath.Join(tmp, "stage")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A real nested file inside stageDir (exercises symlink resolution: tmp on
	// macOS lives under /var → /private/var).
	insideFile := filepath.Join(stageDir, "planning.q1.question.json")
	if err := os.WriteFile(insideFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Sibling directory whose name shares a prefix with stageDir.
	siblingDir := filepath.Join(tmp, "stage-extra")
	if err := os.MkdirAll(siblingDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		file string
		dir  string
		want bool
	}{
		{"file inside dir", insideFile, stageDir, true},
		{"nested file inside dir", filepath.Join(stageDir, "sub", "q.json"), stageDir, true},
		{"file equals dir", stageDir, stageDir, true},
		{"sibling with shared prefix", filepath.Join(siblingDir, "planning.q1.question.json"), stageDir, false},
		{"file completely outside", filepath.Join(tmp, "elsewhere", "q.json"), stageDir, false},
		{"relative file outside", "elsewhere/q.json", stageDir, false},
		{"dir is root", "/private/tmp/x", "/", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathInside(tc.file, tc.dir); got != tc.want {
				t.Errorf("pathInside(%q, %q) = %v, want %v", tc.file, tc.dir, got, tc.want)
			}
		})
	}
}

func TestDetectDialogViolation(t *testing.T) {
	stageDir := t.TempDir()
	outside := filepath.Join(stageDir, "..", "wrong", "planning.q1.question.json")
	inside := filepath.Join(stageDir, "planning.q2.question.json")

	writeEvent := func(path string) string {
		return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":` +
			`"` + path + `"` +
			`,"content":"..."}}]}}`
	}

	tests := []struct {
		name    string
		jsonl   string
		wantOK  bool
		wantSub string
	}{
		{
			name:    "question written outside stageDir",
			jsonl:   writeEvent(outside),
			wantOK:  true,
			wantSub: "dialog protocol violation",
		},
		{
			name:   "question written inside stageDir",
			jsonl:  writeEvent(inside),
			wantOK: false,
		},
		{
			name:   "non-question write ignored",
			jsonl:  `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/tmp/plan.md"}}]}}`,
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// detectDialogViolation scans <phase>.jsonl for all three phases.
			for _, ph := range []string{phasePlanning, phaseImplementation, phaseReview} {
				if err := os.WriteFile(filepath.Join(stageDir, ph+".jsonl"), []byte(tc.jsonl+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			reason, ok := detectDialogViolation(stageDir)
			if ok != tc.wantOK {
				t.Fatalf("detectDialogViolation ok = %v, want %v (reason=%q)", ok, tc.wantOK, reason)
			}
			if tc.wantOK && !strings.Contains(reason, tc.wantSub) {
				t.Errorf("reason %q missing %q", reason, tc.wantSub)
			}
		})
	}
}

// TestDetectDialogViolationMissingLog: no jsonl at all → no violation, no error.
func TestDetectDialogViolationMissingLog(t *testing.T) {
	reason, ok := detectDialogViolation(t.TempDir())
	if ok {
		t.Errorf("detectDialogViolation on empty dir = (%q, true), want false", reason)
	}
}
