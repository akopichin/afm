// Package orchestrator (this file, T5.1): the production wiring between executor.Observation and the Этап 5
// watcher subsystem (watch_tasks.go). observationSink itself does nothing with bus.UIBus — it is a completely
// SEPARATE, synchronous path from uiActionPublisher's best-effort live feed, which is exactly what makes it
// immune to a slow UI subscriber's drops (TestObservation_UIBusDropDoesNotAffectObservations).
package orchestrator

import "github.com/akopichin/afm/pkg/executor"

// observationSink returns the executor.Config.OnObservation callback for a stage's main-agent runner
// (RunPlanning/RunAgent only — see runner_factory.go's two call sites). It fans out, synchronously, to every
// watcher Collector currently bound to stageID — never to bus.UIBus, and never dropped.
func (o *Orchestrator) observationSink(stageID string) func(executor.Observation) {
	return func(obs executor.Observation) {
		o.feedWatchCollectors(stageID, obs)
	}
}
