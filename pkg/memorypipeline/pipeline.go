package memorypipeline

// Pipeline holds the wiring shared by all memory-pipeline operations run
// against one memory-rebuild attempt: the seam that actually runs a step's
// agent (AgentRunner).
type Pipeline struct {
	run AgentRunner
}

// Option customizes a Pipeline built by New.
type Option func(*Pipeline)

// WithRunner overrides the default exec-backed AgentRunner (NewExecRunner)
// — tests inject a stub here instead of spawning a real agent process.
func WithRunner(r AgentRunner) Option {
	return func(p *Pipeline) { p.run = r }
}

// New builds a Pipeline. By default it runs agents via NewExecRunner(agent,
// prompts); overridable via Option (WithRunner) so tests never spawn a real
// process.
func New(prompts Prompts, agent AgentConfig, opts ...Option) *Pipeline {
	p := &Pipeline{
		run: NewExecRunner(agent, prompts),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}
