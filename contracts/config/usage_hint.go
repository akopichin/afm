package config

import "github.com/akopichin/afm/contracts/accounting"

// UsageHint returns the channel/model hint of the docker.agents recipe that cmd resolves to; zero without a recipe.
func (c Config) UsageHint(cmd string) accounting.UsageHint {
	resolved, _ := ResolveAgentCommand(cmd, c)
	recipe, ok := c.Docker.Agents[resolved]
	if !ok {
		return accounting.UsageHint{}
	}
	return accounting.UsageHint{Channel: recipe.Type, Model: recipe.Model}
}
