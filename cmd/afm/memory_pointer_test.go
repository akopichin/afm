package main

import (
	"strings"
	"testing"
)

func TestBuildMemoryPointer_MentionsBothFiles(t *testing.T) {
	got := buildMemoryPointer("/proj/docs/PROJECT_MEMORY.md", "/runs/r1/SESSION_MEMORY.md")
	if !strings.Contains(got, "/proj/docs/PROJECT_MEMORY.md") {
		t.Error("missing project path")
	}
	if !strings.Contains(got, "/runs/r1/SESSION_MEMORY.md") {
		t.Error("missing session path")
	}
}

func TestBuildMemoryPointer_EmptyWhenDisabled(t *testing.T) {
	if buildMemoryPointer("", "/runs/r1/SESSION_MEMORY.md") != "" {
		t.Error("expected empty pointer when project path is empty")
	}
}
