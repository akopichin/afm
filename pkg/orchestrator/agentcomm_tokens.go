package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/executor"
)

// Agent Mesh Phase 3, T3.1: per-invocation bearer tokens for /api/agent-mesh/*. A token is minted fresh for
// EACH actual process invocation (never reused across a retry) by the executor's MeshClient.Mint callback
// (pkg/executor/mesh.go, wired from meshClientFor below) and revoked the moment that invocation's run()
// returns — mirroring the managed browser's per-invocation descriptor lifecycle, not its single
// run-lifetime token. Kept in memory only: a token that outlives the process that minted it is useless (the
// process is gone), and persisting it durably would be a second place a secret could leak from.
type meshTokenStore struct {
	mu     sync.Mutex
	tokens map[string]meshToken
}

// meshToken is the token of a live invocation. stageEpoch is the epoch of the turn the invocation belongs to.
type meshToken struct {
	participant agentcomm.ParticipantID
	stageEpoch  uint64
}

func newMeshTokenStore() *meshTokenStore {
	return &meshTokenStore{tokens: map[string]meshToken{}}
}

func randomMeshToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// mint stores a fresh random token bound to participant and returns it. A read error from crypto/rand is
// never silently swallowed into an empty/predictable token — the caller gets no descriptor instead.
func (s *meshTokenStore) mint(participant agentcomm.ParticipantID, stageEpoch uint64) (string, error) {
	token, err := randomMeshToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.tokens[token] = meshToken{participant: participant, stageEpoch: stageEpoch}
	s.mu.Unlock()
	return token, nil
}

// resolve looks up token, returning the bound participant and whether it is still valid. Never falls back to
// a request-supplied participant id — an unknown/expired token authenticates no one (mirrors
// TestAttach_RejectsClientSuppliedCommand's "server resolves identity, the caller does not supply it").
func (s *meshTokenStore) resolve(token string) (agentcomm.ParticipantID, bool) {
	t, ok := s.live(token)
	return t.participant, ok
}

// live returns token while its invocation runs; false once it has ended, or for a token never minted.
func (s *meshTokenStore) live(token string) (meshToken, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[token]
	return t, ok
}

// revoke invalidates token once its invocation has ended. Idempotent: revoking an already-revoked or
// never-issued token is a no-op.
func (s *meshTokenStore) revoke(token string) {
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
}

// values returns every live token, for redaction (Orchestrator.secretValues).
func (s *meshTokenStore) values() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.tokens))
	for token := range s.tokens {
		out = append(out, token)
	}
	return out
}

type meshInvocationKey struct{}

// WithMeshInvocation returns ctx carrying the bearer token an agent request was authenticated with: Wait decides the
// request against that invocation (still running, same attempt) under the Store lock.
func WithMeshInvocation(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, meshInvocationKey{}, token)
}

// MeshInvocation is the token WithMeshInvocation put in ctx, "" outside an agent request.
func MeshInvocation(ctx context.Context) string {
	token, _ := ctx.Value(meshInvocationKey{}).(string)
	return token
}

// checkInvocation refuses a stage agent's wait whose invocation has ended or belongs to an earlier attempt, under the
// Store lock and after the revocation that precedes the turn's exit. Wait gets only tokens the server authenticated,
// so a stage token that is no longer live is an ended invocation's.
func (o *Orchestrator) checkInvocation(rs state.RunState, req WaitRequest) error {
	stageID, isStage := stageIDFromMeshParticipant(req.Waiter)
	if req.invocation == "" || !isStage {
		return nil
	}
	live, ok := o.meshTokens().live(req.invocation)
	if !ok {
		return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: fmt.Sprintf("the agent invocation of stage %s that sent this wait has ended: it was not registered", stageID)}
	}
	if live.participant != req.Waiter {
		return nil
	}
	if rs.Stages[stageID].Epoch != live.stageEpoch {
		return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: fmt.Sprintf("stage %s was paused, continued or revised after this agent started: its wait belongs to a previous attempt and was not registered", stageID)}
	}
	return nil
}

// ResolveMeshToken implements the agent-facing HTTP layer's identity check (T3.1): the server never trusts a
// client-supplied participant id, only this lookup.
func (o *Orchestrator) ResolveMeshToken(token string) (agentcomm.ParticipantID, bool) {
	return o.meshTokens().resolve(token)
}

func (o *Orchestrator) meshTokens() *meshTokenStore {
	o.meshTokensOnce.Do(func() { o.meshTokensStore = newMeshTokenStore() })
	return o.meshTokensStore
}

// MeshBaseURL returns the agent-facing mesh endpoint base, or "" when the dashboard HTTP server is disabled
// (server.port: 0) — the capability-off signal meshClientFor and the prompt's Available flag both key off.
func (o *Orchestrator) MeshBaseURL() string {
	if o.opts.DashboardURL == "" {
		return ""
	}
	return o.opts.DashboardURL + "/api/agent-mesh"
}

// stageEpochOf is the current execution epoch of participant's stage, 0 for any other participant.
func (o *Orchestrator) stageEpochOf(participant agentcomm.ParticipantID) uint64 {
	stageID, isStage := stageIDFromMeshParticipant(participant)
	if !isStage || o.opts.Store == nil {
		return 0
	}
	return o.mintEpochOf(stageID)
}

// meshClientFor builds the executor.MeshClient for participant, or nil when mesh is unavailable (the
// dashboard HTTP server is disabled — server.port: 0). Minting itself is deferred to Mint, called by
// setupMeshDescriptor inside run() for EACH actual invocation, never here: this function may be called once
// per retry attempt's Config construction, but a token must exist only for the lifetime of the ONE process
// invocation that actually uses it.
func (o *Orchestrator) meshClientFor(participant agentcomm.ParticipantID) *executor.MeshClient {
	base := o.MeshBaseURL()
	if base == "" {
		return nil
	}
	return &executor.MeshClient{
		ParticipantID: string(participant),
		Mint: func() (string, string, func()) {
			token, err := o.meshTokens().mint(participant, o.stageEpochOf(participant))
			if err != nil {
				return "", "", nil
			}
			return base, token, func() { o.meshTokens().revoke(token) }
		},
	}
}
