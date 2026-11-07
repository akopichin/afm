package main

import (
	"encoding/json"
	"testing"
)

func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not one JSON object: %q: %v", s, err)
	}
	return m
}

type stageRun struct {
	stdout, stderr string
	code           int
}
