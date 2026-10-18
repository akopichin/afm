// Package agentcomm defines the shared, neutral vocabulary for the Agent Mesh feature:
// participants, threads, messages, receipts, waits and board entries, plus the protocol version
// and the validation/limits that govern them.
//
// The package is deliberately pure: it imports nothing from pkg/state, pkg/orchestrator,
// pkg/server or pkg/executor (see TestNoForbiddenImports). pkg/state projects this package's
// effects into the durable journal, and pkg/orchestrator implements the Service that both the
// dashboard and the agent's own CLI call through — agentcomm is the contract those layers agree
// on, not an implementation of either of them. This mirrors the role pkg/assistants already
// plays for the collaboration/helpers feature (see pkg/assistants/types.go and its own
// TestNoForbiddenImports): a shared dictionary avoids a state↔orchestrator import cycle without
// either layer owning the vocabulary.
package agentcomm
