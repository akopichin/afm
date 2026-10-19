package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Of two resolutions of one wait, only the one whose commit ended it announces the end and wakes the waiter —
// whether the other is a second CancelWait or the automatic release of a wait nobody can answer.
func TestWaitResolution_OnlyTheWinnerAnnouncesAndWakes(t *testing.T) {
	for _, first := range []string{"cancel", "unanswerable"} {
		t.Run(first+" wins", func(t *testing.T) {
			o, _ := setupHookOrch(t, "s1")
			events := o.critical.Recv()
			parkStageAwaitingPeer(t, o, "s1")
			<-events
			o.prePeerPhase.Store("s1", phaseImplementation)
			receipt, err := o.Wait(context.Background(), WaitRequest{
				OperationID: "op-winner-wait", Waiter: meshStageParticipantID("s1"),
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-not-yet"}, Deadline: testFarDeadline(),
			})
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}

			switch first {
			case "cancel":
				if err := o.CancelWait(context.Background(), CancelWaitRequest{OperationID: "op-cancel-1", WaitID: receipt.WaitID, Actor: state.Actor{Kind: "user"}}); err != nil {
					t.Fatalf("CancelWait #1: %v", err)
				}
			default:
				waiter, changed, err := o.resolveMeshWaitDurable("op-auto", receipt.WaitID, state.Actor{Kind: "system"}, agentcomm.WaitCancelled, "nobody left can answer: test")
				if err != nil || !changed || waiter != meshStageParticipantID("s1") {
					t.Fatalf("automatic cancel = %s, %v, %v; want it to win", waiter, changed, err)
				}
				o.resumeWaiterAfterTerminalWait(context.Background(), waiter)
			}
			waitForCriticalEvent(t, events, bus.EventPeerWaitEnded, "s1", 2*time.Second)

			// The stage is still parked: a second wake would resume it a second time.
			if err := o.CancelWait(context.Background(), CancelWaitRequest{OperationID: "op-cancel-2", WaitID: receipt.WaitID, Actor: state.Actor{Kind: "user"}}); err != nil {
				t.Fatalf("CancelWait #2: %v", err)
			}
			requireNoCriticalEvent(t, events, bus.EventPeerWaitEnded, "s1", 150*time.Millisecond)
			ended := 0
			for _, line := range strings.Split(noticesText(t, o), "\n") {
				if strings.Contains(line, string(receipt.WaitID)) && strings.Contains(line, string(agentcomm.WaitCancelled)) {
					ended++
				}
			}
			if ended != 1 {
				t.Fatalf("notices announcing the cancellation = %d, want 1 (only the winner announces)\n%s", ended, noticesText(t, o))
			}
		})
	}
}
