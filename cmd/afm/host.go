package main

import (
	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm-engine/host"
	"github.com/akopichin/afm/pkg/server"
	"github.com/spf13/cobra"
)

func newAgentCmd() *cobra.Command                                     { return host.NewAgentCommand() }
func newStageCmd() *cobra.Command                                     { return host.NewStageCommand() }
func newBrowserCmd() *cobra.Command                                   { return host.NewBrowserCommand() }
func agentMeshServerActions(mesh engine.Mesh) server.AgentMeshActions { return host.AgentActions(mesh) }

var newAgentLoopCmd = host.NewAgentLoopCmd

var newAgentWhoamiCmd = host.NewAgentWhoamiCmd

var newAgentSendCmd = host.NewAgentSendCmd

var newAgentInboxCmd = host.NewAgentInboxCmd

var newAgentWaitCmd = host.NewAgentWaitCmd

var newAgentWaitStatusCmd = host.NewAgentWaitStatusCmd

var newAgentSpawnCmd = host.NewAgentSpawnCmd

var newAgentHelperResultCmd = host.NewAgentHelperResultCmd

var newAgentBoardCmd = host.NewAgentBoardCmd

var newAgentTaskCmd = host.NewAgentTaskCmd

var toParticipantIDs = host.ToParticipantIDs

var toMeshMessagePage = host.ToMeshMessagePage

var boardTaskRequest = host.BoardTaskRequest

var applyLoopCommand = host.ApplyLoopCommand

type agentMeshActions = host.MeshActions

type agentMeshDescriptor = host.MeshDescriptor

const agentWaitStatusYield = "yield"

const agentWaitStatusReady = "ready"

var newSchemaCmd = host.NewSchemaCmd

var newStopHookCmd = host.NewStopHookCmd
