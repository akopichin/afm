package orchestrator

import (
	"errors"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Classification
	}{
		{"nil", nil, ClassNone},
		{matchRateLimit, errors.New("rate limit exceeded"), ClassRetryable},
		{matchOverloaded, errors.New("overloaded"), ClassRetryable},
		{matchHTTP500, errors.New("http 500 internal server error"), ClassRetryable},
		{"incomplete", &IncompleteWorkError{Reason: "no .done"}, ClassIncomplete},
		{"missing artifact", &MissingArtifactError{Name: "api-contract"}, ClassMissingArtifact},
		{"missing sections", &MissingSectionsError{Missing: []string{sectionAssumptions}}, ClassMissingSections},
		{"storage fatal", &StorageError{Inner: errors.New("disk full")}, ClassStorageFatal},
		{"generic", errors.New("something broke"), ClassFatal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Errorf("Classify(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
