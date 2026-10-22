package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// Agent Mesh Phase 3, T3.2: the afm agent CLI — a hidden command (never meant to be typed by a human) every
// supported executor's inbox/send/wait instructions point at. Owns no state: it is a thin JSON-in/JSON-out
// wrapper over /api/agent-mesh/*, mirroring `afm browser` in shape (Hidden root command, one subcommand per
// op, --descriptor-style config read from env instead of a flag).

// agentWaitStatusYield/agentWaitStatusReady are `afm agent wait`'s two possible "status" values — see
// newAgentWaitCmd's doc comment.
const (
	agentWaitStatusYield = "yield"
	agentWaitStatusReady = "ready"
)

// newAgentCmd registers `afm agent`.
func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "agent",
		Short:  "Agent Mesh client: talk to colleagues over the shared communication service (internal)",
		Hidden: true,
	}
	cmd.AddCommand(
		newAgentWhoamiCmd(), newAgentListCmd(), newAgentSendCmd(), newAgentReplyCmd(),
		newAgentInboxCmd(), newAgentAckCmd(), newAgentWaitCmd(), newAgentBoardCmd(), newAgentTaskCmd(),
	)
	return cmd
}

// runAgentCmd loads the mesh client, runs fn, and renders exactly one JSON object: to stdout on success, or
// {"error":code,"message":...} to stderr followed by a non-zero *ExitError on failure — cobra's own
// "Error: ..." + usage printing is suppressed (Silent: true) so stdout/stderr each carry exactly one JSON
// value, never prose.
func runAgentCmd(cmd *cobra.Command, fn func(c *agentMeshClient) (any, error)) error {
	client, err := loadAgentMeshClient()
	if err == nil {
		var result any
		result, err = fn(client)
		if err == nil {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
	}
	ce := asAgentCLIError(err)
	_ = json.NewEncoder(cmd.ErrOrStderr()).Encode(map[string]string{"error": ce.Code, "message": ce.Message})
	return &ExitError{Code: 1, Silent: true}
}

func newAgentWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "whoami",
		Short:        "Print this invocation's own participant identity",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c *agentMeshClient) (any, error) {
				var out map[string]string
				if err := c.do("GET", "whoami", nil, &out); err != nil {
					return nil, err
				}
				return out, nil
			})
		},
	}
}

func newAgentListCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "list",
		Short:        "List every participant reachable in this run",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c *agentMeshClient) (any, error) {
				var out struct {
					Participants []agentcomm.Participant `json:"participants"`
				}
				if err := c.do("GET", "participants", nil, &out); err != nil {
					return nil, err
				}
				return out, nil
			})
		},
	}
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func newAgentSendCmd() *cobra.Command {
	var thread, to, kind, body, inReplyTo, artifacts string
	var requestID *string
	c := &cobra.Command{
		Use:          "send",
		Short:        "Send a message to one or more colleagues",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c2 *agentMeshClient) (any, error) {
				reqBody := map[string]any{
					"request_id": *requestID, "thread_id": thread, "recipient_ids": splitCSV(to),
					"kind": kind, "body": body, "artifact_refs": splitCSV(artifacts),
				}
				if inReplyTo != "" {
					reqBody["in_reply_to"] = inReplyTo
				}
				var out map[string]any
				if err := c2.do("POST", "send", reqBody, &out); err != nil {
					return nil, err
				}
				return out, nil
			})
		},
	}
	c.Flags().StringVar(&thread, "thread", "", "thread id to send into; omit to start or continue the conversation with --to")
	c.Flags().StringVar(&to, "to", "", "comma-separated recipients: aliases (docs, @docs) or participant ids (required)")
	c.Flags().StringVar(&kind, "kind", string(agentcomm.MessageKindMessage), "message kind: message|request|reply|result|proposal")
	c.Flags().StringVar(&body, "body", "", "message body (required)")
	c.Flags().StringVar(&inReplyTo, "in-reply-to", "", "id of the message this one answers (required when kind=reply)")
	c.Flags().StringVar(&artifacts, "artifacts", "", "comma-separated artifact refs")
	requestID = requestIDFlag(c)
	return c
}

func newAgentReplyCmd() *cobra.Command {
	var thread, to, body, inReplyTo, artifacts string
	c := &cobra.Command{
		Use:          "reply",
		Short:        "Reply to a specific message, preserving its thread",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c2 *agentMeshClient) (any, error) {
				reqBody := map[string]any{
					"request_id": newAgentRequestID(), "thread_id": thread, "recipient_ids": splitCSV(to),
					"body": body, "in_reply_to": inReplyTo, "artifact_refs": splitCSV(artifacts),
				}
				var out map[string]any
				if err := c2.do("POST", "reply", reqBody, &out); err != nil {
					return nil, err
				}
				return out, nil
			})
		},
	}
	c.Flags().StringVar(&thread, "thread", "", "thread id; omit to reply in the thread of --in-reply-to")
	c.Flags().StringVar(&to, "to", "", "comma-separated recipients; omit to answer the sender of --in-reply-to")
	c.Flags().StringVar(&body, "body", "", "reply body (required)")
	c.Flags().StringVar(&inReplyTo, "in-reply-to", "", "id of the message being answered (required)")
	c.Flags().StringVar(&artifacts, "artifacts", "", "comma-separated artifact refs")
	return c
}

func newAgentInboxCmd() *cobra.Command {
	var cursor uint64
	var limit int
	var all bool
	c := &cobra.Command{
		Use:   "inbox",
		Short: "Read NEW messages addressed to this participant (--all for the whole history)",
		Long: "Read the messages addressed to this participant that it has not seen yet — neither in its prompt's <inbox> block " +
			"nor returned by an earlier `afm agent inbox`. Each message is returned once. --all returns every message ever " +
			"addressed to it.",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c2 *agentMeshClient) (any, error) {
				path := "inbox?cursor=" + strconv.FormatUint(cursor, 10) + "&limit=" + strconv.Itoa(limit)
				if all {
					path += "&all=1"
				}
				var out map[string]any
				if err := c2.do("GET", path, nil, &out); err != nil {
					return nil, err
				}
				return out, nil
			})
		},
	}
	c.Flags().Uint64Var(&cursor, "cursor", 0, "only messages after this seq")
	c.Flags().IntVar(&limit, "limit", 0, "max messages to return (0 = server default)")
	c.Flags().BoolVar(&all, "all", false, "every message ever addressed to you, including ones already seen")
	return c
}

func newAgentAckCmd() *cobra.Command {
	var message string
	c := &cobra.Command{
		Use:          "ack",
		Short:        "Acknowledge receipt of a specific message",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c2 *agentMeshClient) (any, error) {
				reqBody := map[string]any{"request_id": newAgentRequestID(), "message_id": message}
				var out map[string]string
				if err := c2.do("POST", "ack", reqBody, &out); err != nil {
					return nil, err
				}
				return out, nil
			})
		},
	}
	c.Flags().StringVar(&message, "message", "", "message id to acknowledge (required)")
	return c
}

// newAgentWaitCmd registers `afm agent wait`. It ALWAYS returns promptly (the Service's Wait never blocks);
// the "status" field tells the caller what to do next: "ready" means the condition already holds (no need
// to end the turn), "yield" means the caller should end its turn now and let the next invocation pick up
// the reply via the <inbox> block or a fresh `afm agent inbox` call.
func newAgentWaitCmd() *cobra.Command {
	var condKind, messageID, threadID, taskID, stageID, key, status, note string
	var deadlineSeconds int64
	c := &cobra.Command{
		Use:          "wait",
		Short:        "Register a wait on a reply/addressed-message/task-result/board-key/stage-status condition",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c2 *agentMeshClient) (any, error) {
				cond := agentcomm.WaitCondition{
					Kind: agentcomm.WaitConditionKind(condKind), MessageID: agentcomm.MessageID(messageID),
					ThreadID: agentcomm.ThreadID(threadID), TaskID: taskID, StageID: stageID, Key: key, Status: status,
				}
				reqBody := map[string]any{"request_id": newAgentRequestID(), "condition": cond, "continuation_note": note}
				if deadlineSeconds > 0 {
					reqBody["deadline_unix"] = time.Now().Unix() + deadlineSeconds
				}
				var out struct {
					WaitID string `json:"wait_id"`
					Status string `json:"status"`
				}
				if err := c2.do("POST", "wait", reqBody, &out); err != nil {
					return nil, err
				}
				cliStatus := agentWaitStatusYield
				if out.Status == string(agentcomm.WaitReady) {
					cliStatus = agentWaitStatusReady
				}
				return map[string]string{"status": cliStatus, "wait_id": out.WaitID}, nil
			})
		},
	}
	c.Flags().StringVar(&condKind, "condition", "", "reply_to|addressed_message|task_result|board_key|stage_status (required)")
	c.Flags().StringVar(&messageID, "message-id", "", "for condition=reply_to")
	c.Flags().StringVar(&threadID, "thread-id", "", "for condition=addressed_message; omit to wait for a new message to you in any thread")
	c.Flags().StringVar(&taskID, "task-id", "", "for condition=task_result")
	c.Flags().StringVar(&stageID, "stage-id", "", "for condition=stage_status")
	c.Flags().StringVar(&key, "key", "", "for condition=board_key")
	c.Flags().StringVar(&status, "status", "", "for condition=board_key or stage_status")
	c.Flags().StringVar(&note, "note", "", "continuation note shown when this participant resumes")
	c.Flags().Int64Var(&deadlineSeconds, "deadline-seconds", 0, "optional deadline, seconds from now (0 = no deadline)")
	return c
}

func newAgentBoardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "board",
		Short: "Read or publish shared board entries",
	}
	cmd.AddCommand(newAgentBoardGetCmd(), newAgentBoardPublishCmd())
	return cmd
}

func newAgentBoardGetCmd() *cobra.Command {
	var key string
	c := &cobra.Command{
		Use:          "get",
		Short:        "Read board entries (all, or one key)",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c2 *agentMeshClient) (any, error) {
				var out map[string]any
				if err := c2.do("GET", withKeyQuery("board", key), nil, &out); err != nil {
					return nil, err
				}
				return out, nil
			})
		},
	}
	c.Flags().StringVar(&key, "key", "", "board key to read (empty = every entry)")
	return c
}

func newAgentBoardPublishCmd() *cobra.Command {
	var key, status, artifacts string
	var expectedVersion int
	var requestID *string
	c := &cobra.Command{
		Use:          "publish",
		Short:        "Publish (or republish) one board entry",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c2 *agentMeshClient) (any, error) {
				reqBody := map[string]any{
					"request_id": *requestID, "key": key, "status": status,
					"artifact_refs": splitCSV(artifacts), "expected_version": expectedVersion,
				}
				var out agentcomm.BoardEntry
				if err := c2.do("POST", "publish", reqBody, &out); err != nil {
					return nil, err
				}
				return out, nil
			})
		},
	}
	c.Flags().StringVar(&key, "key", "", "board key (required)")
	c.Flags().StringVar(&status, "status", "", "the published status value (required)")
	c.Flags().StringVar(&artifacts, "artifacts", "", "comma-separated artifact refs")
	c.Flags().IntVar(&expectedVersion, "expected-version", 0, "0 for a first publish; otherwise the key's current version")
	requestID = requestIDFlag(c)
	return c
}
