package config

import (
	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/skills"
)

// Execution contains only resolved execution settings; transports and product flags stay with the caller.
type Execution struct {
	Client        ClientConfig
	Executor      ExecutorConfig
	Docker        DockerConfig
	PromptsDir    string
	SkillsDir     skills.Dir
	AutoRecover   *bool
	Pricing       accounting.PricingConfig
	Verify        VerifyConfig
	UsageLimit    UsageLimitConfig
	Browser       BrowserConfig
	SideAgent     SideAgentConfig
	Collaboration CollaborationConfig
	AddStage      AddStageConfig
}

// Execution resolves the product configuration into library inputs without UI or server settings.
func (c Config) Execution() Execution {
	return Execution{
		Client:        c.Client,
		Executor:      c.Executor,
		Docker:        c.Docker,
		PromptsDir:    c.PromptsDir,
		SkillsDir:     c.SkillsDir,
		AutoRecover:   c.AutoRecover,
		Pricing:       c.Pricing,
		Verify:        c.Verify,
		UsageLimit:    c.UsageLimit,
		Browser:       c.Browser,
		SideAgent:     c.SideAgent,
		Collaboration: c.Collaboration,
		AddStage:      c.AddStage,
	}
}
func DefaultExecution() Execution { return Default().Execution() }
