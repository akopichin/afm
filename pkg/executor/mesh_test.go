package executor

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// authorMeshClient is a representative author-runner MeshClient whose Mint hands out a fixed token for
// inspection, and records whether revoke was actually called.
func authorMeshClient(revoked *bool) *MeshClient {
	return &MeshClient{
		ParticipantID: "stage:build",
		Mint: func() (string, string, func()) {
			return "http://127.0.0.1:9876/api/agent-mesh", "tok-MESH-SECRET", func() {
				if revoked != nil {
					*revoked = true
				}
			}
		},
	}
}

func TestSetupMeshDescriptor_EnvAndFilePermissions(t *testing.T) {
	var revoked bool
	e := New(Config{Command: "claude", MeshClient: authorMeshClient(&revoked)})
	env, cleanup, err := e.setupMeshDescriptor()
	if err != nil {
		t.Fatal(err)
	}

	descPath := envValue(env, "AFM_MESH_DESCRIPTOR")
	if descPath == "" {
		t.Fatal("AFM_MESH_DESCRIPTOR must be set")
	}
	for _, kv := range env {
		if strings.Contains(kv, "tok-MESH-SECRET") {
			t.Fatalf("token must never appear directly in env, only inside the descriptor file: %q", kv)
		}
	}

	info, err := os.Stat(descPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("descriptor must be 0600, got %v", info.Mode().Perm())
	}
	data, err := os.ReadFile(descPath)
	if err != nil {
		t.Fatal(err)
	}
	var d MeshDescriptor
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	if d.Token != "tok-MESH-SECRET" || d.URL == "" || d.ParticipantID != "stage:build" {
		t.Fatalf("descriptor contents = %+v, want token/url/participant_id populated", d)
	}

	if revoked {
		t.Fatal("revoke must not run before cleanup is called")
	}
	cleanup()
	if !revoked {
		t.Fatal("cleanup must revoke the per-invocation token")
	}
	if _, err := os.Stat(descPath); !os.IsNotExist(err) {
		t.Fatalf("cleanup must remove the descriptor temp dir, stat err = %v", err)
	}
}

func TestSetupMeshDescriptor_MintFailureFailsClosed(t *testing.T) {
	e := New(Config{Command: "claude", MeshClient: &MeshClient{
		ParticipantID: "stage:build",
		Mint:          func() (string, string, func()) { return "", "", nil },
	}})
	_, _, err := e.setupMeshDescriptor()
	if err == nil {
		t.Fatal("a Mint that cannot produce a token must fail closed, got nil error")
	}
}

func TestFilterInheritedEnv_StripsMeshTransport(t *testing.T) {
	ambient := []string{"AFM_MESH_DESCRIPTOR=/tmp/x", "AFM_MESH_TOKEN=y", "PATH=/bin"}
	for _, cfg := range []Config{
		{VerifyMode: true}, // AI verify — never gets a mesh descriptor
		{},                 // JSON-fix / memory-pipeline / lifecycle hook / plain author with no MeshClient
	} {
		e := New(cfg)
		out := e.filterInheritedEnv(ambient)
		for _, kv := range out {
			if IsMeshTransportEnv(kv) {
				t.Fatalf("every child env must be stripped of AFM_MESH_* (cfg=%+v): %q", cfg, kv)
			}
		}
	}
}
