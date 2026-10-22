package state

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

// meshBoardProtocolSetPayload is the payload of a mesh_board_protocol_set control record.
type meshBoardProtocolSetPayload struct {
	Protocol agentcomm.BoardTaskProtocol `json:"protocol"`
}

// RecordBoardTaskProtocol durably marks a fresh run with its board task protocol before any agent starts.
// It is a no-op when a protocol is already recorded. A run that already has journal records but no marker
// is a legacy run: it is refused, because resuming must never migrate it to the typed protocol.
func (s *Store) RecordBoardTaskProtocol(p agentcomm.BoardTaskProtocol, actor Actor) error {
	snap := s.Snapshot()
	if snap.Assistants.Mesh.BoardTaskProtocol != "" {
		return nil
	}
	if snap.LastSeq > 0 {
		return &OperationRejectedError{
			Code:   assistants.CodeInvalidStatus,
			Reason: "board task protocol cannot be recorded for a run that already has a journal",
		}
	}

	payload, err := json.Marshal(meshBoardProtocolSetPayload{Protocol: p})
	if err != nil {
		return fmt.Errorf("marshal mesh_board_protocol_set payload: %w", err)
	}
	c := &Control{Kind: kindMeshBoardProtocolSet, SchemaVersion: ControlSchemaVersion, Actor: actor, Payload: payload}
	return s.AppendControl(c)
}

func applyMeshBoardProtocolSet(as *AssistantsState, c *Control, dryRun bool) error {
	var p meshBoardProtocolSetPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode mesh_board_protocol_set payload: %w", err)
	}
	if p.Protocol == "" {
		return errors.New("mesh_board_protocol_set: empty protocol")
	}
	if !dryRun {
		as.Mesh.BoardTaskProtocol = p.Protocol
	}
	return nil
}
