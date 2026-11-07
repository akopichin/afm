package agentcomm

import (
	"strings"

	"github.com/akopichin/afm/contracts/assistants"
)

// HelperParticipantPrefix starts the participant id of a stage helper task, "helper:<taskID>". The convention
// lives here so that pkg/state can map a participant to its task without importing the orchestrator.
const HelperParticipantPrefix = "helper:"

// HelperParticipantID is the mesh identity of the stage helper task taskID.
func HelperParticipantID(taskID assistants.TaskID) ParticipantID {
	return ParticipantID(HelperParticipantPrefix + string(taskID))
}

// HelperTaskID is the task behind a stage helper's participant id; false for any other participant.
func HelperTaskID(id ParticipantID) (assistants.TaskID, bool) {
	taskID, ok := strings.CutPrefix(string(id), HelperParticipantPrefix)
	return assistants.TaskID(taskID), ok && taskID != ""
}
