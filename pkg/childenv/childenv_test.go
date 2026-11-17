package childenv

import (
	"slices"
	"testing"
)

func TestStripTransportDropsSecretTransport(t *testing.T) {
	env := []string{
		"PATH=/bin",
		"AFM_SECRET_TOKEN=s",
		"AFM_SYSPROMPT_X=p",
		"AFM_HOOK_SECRET_K=h",
		"AFM_BROWSER_ENDPOINT=e",
		"AFM_IN_DOCKER=1",
		"AFM_STAGE_DIR=/run/s",
	}
	got := StripTransport(env)
	want := []string{"PATH=/bin", "AFM_IN_DOCKER=1", "AFM_STAGE_DIR=/run/s"}
	if !slices.Equal(got, want) {
		t.Errorf("StripTransport = %v, want %v", got, want)
	}
}

func TestForAFMChildDropsContainerAndRunScopedVars(t *testing.T) {
	dropped := []string{
		"AFM_IN_DOCKER=1",
		"AFM_STAGE_DIR=/run/s",
		"AFM_STAGE_ID=plan",
		"AFM_STAGE_NAME=Plan",
		"AFM_STAGE_FROM=a",
		"AFM_STAGE_TO=b",
		"AFM_DIALOG_PHASE=plan",
		"AFM_SCRATCH_DIR=/tmp/s",
		"AFM_OUTPUT_SCHEMA_FILE=/tmp/schema.json",
		"AFM_STREAM_EXTENSION=1",
		"AFM_MESH_DESCRIPTOR=/tmp/mesh.json",
		"AFM_MESH_TOKEN=t",
		"AFM_STOP_HOOK_DESCRIPTOR=/tmp/stop.json",
		"AFM_STOP_HOOK_CODEX_STATE=/tmp/state",
		"AFM_BROWSER_ENDPOINT=e",
		"AFM_SECRET_TOKEN=s",
		"AFM_SYSPROMPT_X=p",
		"AFM_HOOK_SECRET_K=h",
		"AFM_HOOK_EVENT=stage_done",
		"AFM_RUN_DIR=/run",
		"AFM_ROOT_DIR=/proj",
		"AFM_FLOW_CYCLE=2",
		"AFM_PIPELINE_DIR=/pipe",
	}
	kept := []string{
		"PATH=/bin",
		"HOME=/home/u",
		"AFM_DIR=/proj",
		"AFM_USE_DOCKER=1",
		"AFM_SIDEAGENT=0",
		"CLAUDE_CODE_OAUTH_TOKEN=t",
	}
	got := ForAFMChild(slices.Concat(kept[:2], dropped, kept[2:]))
	if !slices.Equal(got, kept) {
		t.Errorf("ForAFMChild = %v, want %v", got, kept)
	}
}
