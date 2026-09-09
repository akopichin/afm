package memorypipeline

import (
	"strings"
	"testing"
)

const validDataset = `
project_level:
  - prompt: "How should we structure config?"
    chosen: "Use a single config.yaml with layered overlays"
    rejected: "Scatter config across env vars"
    source: log
session_level:
  - prompt: "What did the user prefer for logging?"
    chosen: "Structured JSON logs"
    rejected: "Plain text logs"
    source: user
`

func TestValidateDataset_Valid(t *testing.T) {
	if err := ValidateDataset([]byte(validDataset)); err != nil {
		t.Fatalf("ValidateDataset(valid) = %v, want nil", err)
	}
	ds, err := ParseDataset([]byte(validDataset))
	if err != nil {
		t.Fatalf("ParseDataset(valid) = %v, want nil", err)
	}
	if len(ds.ProjectLevel) != 1 || len(ds.SessionLevel) != 1 {
		t.Fatalf("ParseDataset(valid) = %+v, want 1+1 items", ds)
	}
	if ds.ProjectLevel[0].Source != "log" || ds.SessionLevel[0].Source != "user" {
		t.Errorf("ParseDataset(valid) sources = %q/%q", ds.ProjectLevel[0].Source, ds.SessionLevel[0].Source)
	}
}

func TestValidateDataset_BothSectionsEmptyIsValid(t *testing.T) {
	data := []byte("project_level: []\nsession_level: []\n")
	if err := ValidateDataset(data); err != nil {
		t.Fatalf("ValidateDataset(empty sections) = %v, want nil", err)
	}
}

func TestValidateDataset_MissingSessionLevelRejected(t *testing.T) {
	data := []byte("project_level: []\n")
	err := ValidateDataset(data)
	if err == nil {
		t.Fatal("ValidateDataset(missing session_level) = nil, want error")
	}
	if !strings.Contains(err.Error(), "session_level") && !strings.Contains(err.Error(), "required") {
		t.Errorf("error %q does not mention the missing section", err)
	}
}

func TestValidateDataset_MissingProjectLevelRejected(t *testing.T) {
	data := []byte("session_level: []\n")
	err := ValidateDataset(data)
	if err == nil {
		t.Fatal("ValidateDataset(missing project_level) = nil, want error")
	}
	if !strings.Contains(err.Error(), "project_level") && !strings.Contains(err.Error(), "required") {
		t.Errorf("error %q does not mention the missing section", err)
	}
}

func TestValidateDataset_UnknownTopKeyRejected(t *testing.T) {
	data := []byte("project_level: []\nsession_level: []\nextra_stuff: []\n")
	if err := ValidateDataset(data); err == nil {
		t.Fatal("ValidateDataset(unknown top key) = nil, want error")
	}
}

func TestValidateDataset_BadSourceRejected(t *testing.T) {
	data := []byte(`
project_level:
  - prompt: "p"
    chosen: "c"
    rejected: "r"
    source: maybe
session_level: []
`)
	err := ValidateDataset(data)
	if err == nil {
		t.Fatal("ValidateDataset(bad source) = nil, want error")
	}
	if !strings.Contains(err.Error(), "source") {
		t.Errorf("error %q does not mention source", err)
	}
}

func TestValidateDataset_EmptyTrimmedFieldRejected(t *testing.T) {
	data := []byte(`
project_level:
  - prompt: "   "
    chosen: "c"
    rejected: "r"
    source: log
session_level: []
`)
	err := ValidateDataset(data)
	if err == nil {
		t.Fatal("ValidateDataset(blank prompt) = nil, want error")
	}
}

func TestValidateDataset_TrailingDocumentRejected(t *testing.T) {
	data := []byte("project_level: []\nsession_level: []\n---\nfoo: bar\n")
	err := ValidateDataset(data)
	if err == nil {
		t.Fatal("ValidateDataset(trailing document) = nil, want error")
	}
	if !strings.Contains(err.Error(), "one YAML document") && !strings.Contains(err.Error(), "document") {
		t.Errorf("error %q does not mention the trailing document", err)
	}
}

func TestValidateDataset_OversizeRejected(t *testing.T) {
	huge := make([]byte, maxDatasetBytes+1)
	for i := range huge {
		huge[i] = 'a'
	}
	err := ValidateDataset(huge)
	if err == nil {
		t.Fatal("ValidateDataset(oversize) = nil, want error")
	}
}

func TestValidateDataset_BareScalarDocumentRejected(t *testing.T) {
	data := []byte("just a string\n")
	err := ValidateDataset(data)
	if err == nil {
		t.Fatal("ValidateDataset(bare scalar) = nil, want error")
	}
	if !strings.Contains(err.Error(), "mapping") {
		t.Errorf("error %q does not mention the mapping requirement", err)
	}
}
