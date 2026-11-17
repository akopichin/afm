// Package runregistry publishes one record per running afm process so a host can find runs it did not start.
package runregistry

import (
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"time"
)

// SchemaVersion is the record format; readers skip records with another version.
const SchemaVersion = 1

// MaxPreviousRunIDs bounds the run history a repeating process keeps in its record.
const MaxPreviousRunIDs = 20

var instanceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Project says where a run's project lives. Worktree is empty when the run works in the project root.
type Project struct {
	Root     string `json:"root"`
	RunName  string `json:"run_name,omitempty"`
	Worktree string `json:"worktree,omitempty"`
}

// Record describes one afm process. It carries no secrets and no workflow state: a host reads state from the
// process's own API.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	InstanceID    string `json:"instance_id"`
	// LaunchID ties the record to a host-started launch; it is empty for runs started by hand.
	LaunchID string `json:"launch_id,omitempty"`
	PID      int    `json:"pid"`
	// ProcessStart tells this process from a later one that reuses its PID.
	ProcessStart string  `json:"process_start,omitempty"`
	AFMVersion   string  `json:"afm_version,omitempty"`
	Mode         string  `json:"mode,omitempty"`
	Docker       bool    `json:"docker,omitempty"`
	Project      Project `json:"project"`
	RunsDir      string  `json:"runs_dir,omitempty"`
	// APIURL is empty until the HTTP server is listening.
	APIURL         string    `json:"api_url,omitempty"`
	MCP            bool      `json:"mcp,omitempty"`
	Dashboard      bool      `json:"dashboard,omitempty"`
	RunID          string    `json:"run_id,omitempty"`
	PreviousRunIDs []string  `json:"previous_run_ids,omitempty"`
	Ready          bool      `json:"ready,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	// UpdatedAt is a last-seen heartbeat, not proof that the process is healthy.
	UpdatedAt time.Time `json:"updated_at"`
}

// Dir is the registry directory under the user's home.
func Dir(home string) string {
	return filepath.Join(home, ".afm", "active-runs")
}

// NewInstanceID returns a fresh random process id of 32 hex characters.
func NewInstanceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("runregistry: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// ValidInstanceID reports whether id has the NewInstanceID shape; it is also what makes it safe as a file name.
func ValidInstanceID(id string) bool {
	return instanceIDPattern.MatchString(id)
}
