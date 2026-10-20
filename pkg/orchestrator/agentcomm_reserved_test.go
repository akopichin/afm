package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

func TestMesh_ClientCannotPreclaimSideTerminalOperationIDs(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	actor := state.Actor{Kind: "user"}
	for _, prefix := range []string{"side-mesh-reply:", "side-mesh-unprocessed:"} {
		for name, call := range map[string]func(string) error{
			"open": func(id string) error {
				_, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: id, Actor: actor})
				return err
			},
			"close": func(id string) error {
				return o.CloseThread(ctx, CloseThreadRequest{OperationID: id, Actor: actor})
			},
			"send": func(id string) error {
				_, err := o.Send(ctx, SendRequest{OperationID: id, Actor: actor})
				return err
			},
			"send from agent": func(id string) error {
				_, err := o.SendFromAgent(ctx, SendRequest{OperationID: id, Actor: actor})
				return err
			},
			"ack": func(id string) error {
				return o.Acknowledge(ctx, AcknowledgeRequest{OperationID: id, Actor: actor})
			},
			"wait": func(id string) error {
				_, err := o.Wait(ctx, WaitRequest{OperationID: id, Actor: actor})
				return err
			},
			"cancel wait": func(id string) error {
				return o.CancelWait(ctx, CancelWaitRequest{OperationID: id, Actor: actor})
			},
			"publish": func(id string) error {
				_, err := o.Publish(ctx, PublishRequest{OperationID: id, Actor: actor})
				return err
			},
			"attach": func(id string) error {
				_, err := o.AttachAndSend(ctx, AttachAndSendRequest{OperationID: id, Actor: actor})
				return err
			},
			"discussion": func(id string) error {
				_, err := o.PostDiscussionMessage(ctx, PostMessageRequest{
					OperationID: id, StageID: assistantsTestStageID,
					AuthorKind: assistants.AuthorHuman, Round: assistants.RoundFreeform,
					Body: "question", Actor: actor,
				})
				return err
			},
		} {
			id := prefix + "source-" + name
			if err := call(id); err == nil {
				t.Fatalf("%s accepted %q", name, id)
			}
			if _, ok := store.Snapshot().Assistants.OperationIndex[id]; ok {
				t.Fatalf("%s preclaimed %q", name, id)
			}
		}
	}
	// The internal operation remains available to the system after the
	// public paths reject both namespaces.
	if _, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "side-mesh-reply:internal", Scope: agentcomm.ScopeRun,
		Members: []agentcomm.ParticipantID{"user"}, Actor: state.Actor{Kind: "system"},
	}); err != nil {
		t.Fatalf("system operation rejected: %v", err)
	}
}

func TestOtherCallerIDsCannotPreclaimSideTerminalOperations(t *testing.T) {
	ctx := context.Background()
	actor := state.Actor{Kind: "user"}
	for _, prefix := range []string{"side-mesh-reply:", "side-mesh-unprocessed:"} {
		t.Run(prefix, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			id := prefix + "source"
			_, err := o.CreateProposal(ctx, CreateProposalRequest{
				OperationID: id, StageID: assistantsTestStageID,
				Type: assistants.ProposalFeedback, Title: "feedback",
				Payload: json.RawMessage(`{"feedback":"check the result"}`),
				Authors: []assistants.MessageAuthor{{Kind: assistants.AuthorHuman}}, Actor: actor,
			})
			assertReservedIDRejected(t, err, "create proposal")
			if _, claimed := store.Snapshot().Assistants.OperationIndex[id]; claimed {
				t.Fatalf("proposal claimed reserved operation ID %q", id)
			}

			cmd := newSafeAgentCommand(t)
			graphOrch, graphStore := newProposalDeliveryTestOrchestrator(t, cmd, []flow.Stage{autoStage("impl", cmd)})
			draft := AddStageDraft{
				Kind: AddStageKindAppend, RequestID: id,
				Stage: graph.StageDraft{ID: "new-stage", Command: cmd, Prompt: "work"},
			}
			if _, err := graphOrch.PreviewAddStage(draft); err != nil {
				t.Fatalf("valid graph draft rejected by preview: %v", err)
			}
			_, err = graphOrch.CommitAddStage(ctx, draft)
			assertReservedIDRejected(t, err, "commit graph")
			if _, claimed := graphStore.Snapshot().Assistants.OperationIndex[id]; claimed {
				t.Fatalf("graph commit claimed reserved operation ID %q", id)
			}
		})
	}
}

func assertReservedIDRejected(t *testing.T, err error, action string) {
	t.Helper()
	var rejected *state.OperationRejectedError
	if !errors.As(err, &rejected) || rejected.Code != assistants.CodeInvalidRequest {
		t.Fatalf("%s with reserved ID: error = %v, want invalid_request", action, err)
	}
}

func TestDashboardActionsCannotPreclaimSideTerminalOperationIDs(t *testing.T) {
	ctx := context.Background()
	actor := state.Actor{Kind: "user"}
	for _, prefix := range []string{"side-mesh-reply:", "side-mesh-unprocessed:"} {
		for _, action := range []string{"attach", "accept proposal", "dismiss proposal"} {
			t.Run(prefix+action, func(t *testing.T) {
				o, store := newAssistantsTestOrchestrator(t, true)
				id := prefix + "source"
				var call func(string) error
				switch action {
				case "attach":
					call = func(opID string) error {
						_, err := o.Attach(ctx, AttachRequest{
							OperationID: opID, StageID: assistantsTestStageID, ProfileID: "reviewer",
							Title: "review", Instruction: "review this stage", Actor: actor,
						})
						return err
					}
				default:
					proposalID, err := o.CreateProposal(ctx, CreateProposalRequest{
						OperationID: "create-" + action, StageID: assistantsTestStageID,
						Type: assistants.ProposalFeedback, Title: "feedback",
						Payload: json.RawMessage(`{"feedback":"check the result"}`),
						Authors: []assistants.MessageAuthor{{Kind: assistants.AuthorHuman}}, Actor: actor,
					})
					if err != nil {
						t.Fatalf("create proposal: %v", err)
					}
					if action == "accept proposal" {
						call = func(opID string) error { return o.AcceptProposal(ctx, proposalID, opID, actor) }
					} else {
						call = func(opID string) error { return o.DismissProposal(ctx, proposalID, opID, "not needed", actor) }
					}
				}
				var rejected *state.OperationRejectedError
				if err := call(id); !errors.As(err, &rejected) || rejected.Code != assistants.CodeInvalidRequest {
					t.Fatalf("%s with reserved ID: error = %v, want invalid_request", action, err)
				}
				if _, claimed := store.Snapshot().Assistants.OperationIndex[id]; claimed {
					t.Fatalf("%s claimed reserved operation ID %q", action, id)
				}
				if err := call("ordinary-" + action); err != nil {
					t.Fatalf("valid %s rejected after reserved ID: %v", action, err)
				}
			})
		}
	}
}
