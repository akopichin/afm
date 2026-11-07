package main

import (
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/lifecyclehooks"
)

func combineRunHooks(cfg config.Config, f *flow.Flow) []lifecyclehooks.RegisteredHook {
	layers := []lifecyclehooks.Layer{{Hooks: cfg.Hooks}, {Hooks: f.Hooks}}
	for _, st := range f.Stages {
		if len(st.Hooks) > 0 {
			layers = append(layers, lifecyclehooks.Layer{StageID: st.ID, Hooks: st.Hooks})
		}
	}
	return lifecyclehooks.Combine(layers...)
}
