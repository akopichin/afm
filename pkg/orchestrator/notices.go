package orchestrator

import (
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

// publishNotice sends the same payload to live clients and the replay log.
// Both deliveries are best-effort; neither changes stage state. FSM events
// and critical completion messages use their own durable/control paths.
func (o *Orchestrator) publishNotice(ev bus.Event) {
	o.ui.Publish(ev)
	stagefiles.AppendNotice(o.opts.RunDir, ev.StageID, string(ev.Type), ev.Data)
}
