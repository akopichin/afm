package memorypipeline

import "time"

// Pipeline holds the wiring shared by all memory-pipeline operations run
// against one memory-rebuild attempt: the compiled prompts, the agent
// launch config, the seam that actually runs a step's agent (AgentRunner),
// and a clock seam for deterministic tests.
type Pipeline struct {
	prompts Prompts
	agent   AgentConfig
	run     AgentRunner
	clock   func() time.Time
}

// Option customizes a Pipeline built by New.
type Option func(*Pipeline)

// WithRunner overrides the default exec-backed AgentRunner (NewExecRunner)
// — tests inject a stub here instead of spawning a real agent process.
func WithRunner(r AgentRunner) Option {
	return func(p *Pipeline) { p.run = r }
}

// WithClock overrides the default clock (time.Now) — tests inject a fixed
// or stepped clock for deterministic timestamps.
func WithClock(c func() time.Time) Option {
	return func(p *Pipeline) { p.clock = c }
}

// New builds a Pipeline. By default it runs agents via NewExecRunner(agent,
// prompts) and reads the time via time.Now; both are overridable via
// Option (WithRunner/WithClock) so tests never spawn a real process.
func New(prompts Prompts, agent AgentConfig, opts ...Option) *Pipeline {
	p := &Pipeline{
		prompts: prompts,
		agent:   agent,
		run:     NewExecRunner(agent, prompts),
		clock:   time.Now,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}
