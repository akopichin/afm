package server

import (
	"net/http"

	"github.com/akopichin/afm/contracts/agentapi"
)

const maxAgentMeshBodyBytes = agentapi.MaxAgentMeshBodyBytes

type AgentMeshActions = agentapi.AgentMeshActions
type MeshSendRequest = agentapi.MeshSendRequest
type MeshSendReceipt = agentapi.MeshSendReceipt
type MeshMessagePage = agentapi.MeshMessagePage
type MeshWaitRequest = agentapi.MeshWaitRequest
type MeshWaitReceipt = agentapi.MeshWaitReceipt
type MeshWaitItem = agentapi.MeshWaitItem
type MeshCancelWaitRequest = agentapi.MeshCancelWaitRequest
type MeshSpawnRequest = agentapi.MeshSpawnRequest
type MeshSpawnReceipt = agentapi.MeshSpawnReceipt
type MeshHelperResult = agentapi.MeshHelperResult
type MeshHelperRequest = agentapi.MeshHelperRequest

var MeshHelperResultOf = agentapi.MeshHelperResultOf

type MeshPublishRequest = agentapi.MeshPublishRequest
type MeshBoardTaskRequest = agentapi.MeshBoardTaskRequest

var decodeBoardTaskBody = agentapi.DecodeBoardTaskBody

const MeshLoopAdd = agentapi.MeshLoopAdd
const MeshLoopStop = agentapi.MeshLoopStop

type MeshLoopCommand = agentapi.MeshLoopCommand

var decodeLoopBody = agentapi.DecodeLoopBody
var loopsForClient = agentapi.LoopsForClient
var boardTasksForClient = agentapi.BoardTasksForClient
var WithMeshToken = agentapi.WithMeshToken

var decodeAgentMeshBody = agentapi.DecodeAgentMeshBody
var parseCursorLimit = agentapi.ParseCursorLimit
var writeJSON = agentapi.WriteJSON

func (s *Server) routeAgentMesh(w http.ResponseWriter, r *http.Request) {
	agentapi.New(s.agentMesh, s.writeAssistantError).ServeHTTP(w, r)
}
