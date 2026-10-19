package main

import (
	"encoding/json"
	"testing"

	"github.com/akopichin/afm/pkg/orchestrator"
)

// An empty stage feed answers with empty lists, not null: the dashboard appends them as they are.
func TestToMeshStagePage_EmptyListsNotNull(t *testing.T) {
	data, err := json.Marshal(toMeshStagePage(orchestrator.StageMessagePage{NextCursor: 7}))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), `{"messages":[],"threads":[],"next_cursor":7,"has_more":false}`; got != want {
		t.Fatalf("page = %s, want %s", got, want)
	}
}
