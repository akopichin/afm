package executor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// errNoMeshToken is returned when MeshClient.Mint could not produce a usable token — e.g. a crypto/rand
// failure. The invocation proceeds WITHOUT a descriptor rather than with a broken one.
var errNoMeshToken = errors.New("executor: mesh client did not mint a usable token")

// MeshClient describes the Agent Mesh capability to inject for an author runner (Agent Mesh Phase 3, T3.3).
// Unlike BrowserMCP (a static per-stage descriptor), Mint is called fresh inside run() for every actual
// process invocation: a mesh bearer token is minted at spawn time and revoked the moment this invocation
// ends, never reused across a retry — mirroring how setupBrowserMCP's own temp descriptor directory is
// scoped to one invocation, but adding the mint/revoke step browser's single run-lifetime token doesn't need.
type MeshClient struct {
	ParticipantID string
	// Mint returns the mesh API base URL, a fresh per-invocation bearer token, and a revoke func to call
	// once this invocation ends (deferred right after Mint in setupMeshDescriptor). A Mint that cannot
	// produce a token (e.g. crypto/rand failure) returns an empty url so the caller fails closed instead of
	// handing the agent a descriptor that will never authenticate.
	Mint func() (url, token string, revoke func())
}

// MeshDescriptor is the 0600 JSON file afm agent reads (AFM_MESH_DESCRIPTOR) — the token never appears
// anywhere else (argv, logs, notices, the agent's own ambient environment).
type MeshDescriptor struct {
	URL           string `json:"url"`
	Token         string `json:"token"`
	ParticipantID string `json:"participant_id"`
}

// meshDescriptorEnv is the env var naming the descriptor file's PATH — never the token itself.
const meshDescriptorEnv = "AFM_MESH_DESCRIPTOR"

// IsMeshTransportEnv reports whether an env entry is an Agent Mesh transport variable, stripped from every
// child environment by filterInheritedEnv and re-added only for the author runner that genuinely received a
// MeshClient (mirrors IsBrowserTransportEnv).
func IsMeshTransportEnv(kv string) bool {
	return strings.HasPrefix(kv, "AFM_MESH_")
}

// setupMeshDescriptor mints a fresh per-invocation token via e.cfg.MeshClient.Mint and writes the 0600
// descriptor file in a new temp dir — mirrors setupBrowserMCP's shape exactly. cleanup both removes the temp
// dir AND revokes the token, so a token's lifetime is bounded to exactly one invocation regardless of how
// that invocation ends (success, crash, timeout, Pause).
func (e *Executor) setupMeshDescriptor() (env []string, cleanup func(), err error) {
	m := e.cfg.MeshClient
	url, token, revoke := m.Mint()
	if url == "" || token == "" {
		if revoke != nil {
			revoke()
		}
		return nil, nil, errNoMeshToken
	}

	dir, err := os.MkdirTemp("", "afm-mesh-inv-")
	if err != nil {
		revoke()
		return nil, nil, err
	}
	descPath := filepath.Join(dir, "descriptor.json")
	cleanup = func() {
		revoke()
		_ = os.RemoveAll(dir)
	}
	if werr := writeMeshDescriptor(descPath, MeshDescriptor{URL: url, Token: token, ParticipantID: m.ParticipantID}); werr != nil {
		cleanup()
		return nil, nil, werr
	}
	return []string{meshDescriptorEnv + "=" + descPath}, cleanup, nil
}

func writeMeshDescriptor(path string, d MeshDescriptor) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
