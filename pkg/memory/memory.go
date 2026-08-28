package memory

import "regexp"

const (
	ScopeProject = "project"
	ScopeSession = "session"

	KindFact         = "fact"
	KindBestPractice = "best_practice"
	KindAntiPattern  = "anti_pattern"
)

// Finding represents a single finding with metadata.
type Finding struct {
	ID           string   `yaml:"id"`
	Scope        string   `yaml:"scope"`
	Kind         string   `yaml:"kind"`
	Topic        []string `yaml:"topic,omitempty"`
	Statement    string   `yaml:"statement"`
	Evidence     string   `yaml:"evidence"`
	FirstSeen    string   `yaml:"first_seen"`
	LastSeen     string   `yaml:"last_seen"`
	ConfirmCount int      `yaml:"confirm_count"`
	SourceStage  string   `yaml:"source_stage,omitempty"`
}

// Store holds a collection of findings.
type Store struct {
	Findings []Finding `yaml:"findings"`
}

var idRegexp = regexp.MustCompile(`^[a-z0-9-]+$`)

// Valid checks if a finding is valid.
func (f Finding) Valid() bool {
	if f.ID == "" {
		return false
	}
	if !idRegexp.MatchString(f.ID) {
		return false
	}
	if f.Scope != ScopeProject && f.Scope != ScopeSession {
		return false
	}
	if f.Kind != KindFact && f.Kind != KindBestPractice && f.Kind != KindAntiPattern {
		return false
	}
	if f.Statement == "" {
		return false
	}
	if f.Evidence == "" {
		return false
	}
	return true
}

// Sanitize returns a copy of the store keeping only valid findings.
func (s Store) Sanitize() Store {
	var valid []Finding
	for _, f := range s.Findings {
		if f.Valid() {
			valid = append(valid, f)
		}
	}
	return Store{Findings: valid}
}
