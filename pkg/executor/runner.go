package executor

import "context"

// Runner is the interface for running AI agents. Allows substitution for tests.
type Runner interface {
	RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error
	RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error
}

// compile-time check that Executor implements Runner.
var _ Runner = (*Executor)(nil)
