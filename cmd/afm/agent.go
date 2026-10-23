package main

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/server"
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

// agentWaitReplyToAll is the --condition of a batch wait. The service knows no such condition: it is reply_to
// with a list of message ids.
const agentWaitReplyToAll = "reply_to_all"

// agentWaitStatusName is the `afm agent wait-status` command and its /api/agent-mesh route.
const agentWaitStatusName = "wait-status"

// newAgentCmd registers `afm agent`.
func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "agent",
		Short:  "Agent Mesh client: talk to colleagues over the shared communication service (internal)",
		Hidden: true,
	}
	cmd.AddCommand(
		newAgentWhoamiCmd(), newAgentListCmd(), newAgentSendCmd(), newAgentReplyCmd(),
		newAgentInboxCmd(), newAgentAckCmd(), newAgentWaitCmd(), newAgentWaitStatusCmd(), newAgentSpawnCmd(), newAgentHelperResultCmd(), newAgentBoardCmd(), newAgentTaskCmd(),
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
//
// --condition reply_to_all --message-ids a,b,c (also spelled --condition reply_to --message-ids a,b,c) is ONE
// wait for the reply to every listed request: it answers with a batch_id and the outcome of each item, and the
// agent is resumed once after all of them ended.
func newAgentWaitCmd() *cobra.Command {
	var condKind, messageID, messageIDs, threadID, taskID, stageID, key, status, note string
	var deadlineSeconds int64
	c := &cobra.Command{
		Use:   "wait",
		Short: "Register a wait on a reply/addressed-message/task-result/board-key/stage-status condition",
		Long: "Register a durable wait, then end your turn. --condition reply_to_all --message-ids a,b,c waits for the reply to " +
			"EACH listed request (up to 16 distinct ids of requests you sent) with one deadline for the whole set: you are resumed " +
			"once, after every one of them was answered, cancelled or expired. Read each item's outcome and the reason an item " +
			"ended without an answer with `afm agent wait-status`; the answers are in your inbox.",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c2 *agentMeshClient) (any, error) {
				cond := agentcomm.WaitCondition{
					Kind: agentcomm.WaitConditionKind(condKind), MessageID: agentcomm.MessageID(messageID),
					ThreadID: agentcomm.ThreadID(threadID), TaskID: taskID, StageID: stageID, Key: key, Status: status,
				}
				reqBody := map[string]any{"request_id": newAgentRequestID(), "condition": cond, "continuation_note": note}
				batch := condKind == agentWaitReplyToAll || (condKind == string(agentcomm.WaitOnReplyTo) && messageIDs != "")
				if batch {
					ids, err := batchMessageIDs(messageID, messageIDs)
					if err != nil {
						return nil, err
					}
					cond.Kind = agentcomm.WaitOnReplyTo
					reqBody["condition"], reqBody["message_ids"] = cond, ids
				} else if messageIDs != "" {
					return nil, newAgentCLIError("invalid_request", "--message-ids goes with --condition %s (or reply_to)", agentWaitReplyToAll)
				}
				if deadlineSeconds > 0 {
					reqBody["deadline_unix"] = time.Now().Unix() + deadlineSeconds
				}
				var out server.MeshWaitReceipt
				if err := c2.do("POST", "wait", reqBody, &out); err != nil {
					return nil, err
				}
				cliStatus := agentWaitStatusYield
				if out.Status == string(agentcomm.WaitReady) {
					cliStatus = agentWaitStatusReady
				}
				if batch {
					return map[string]any{"status": cliStatus, "batch_id": out.BatchID, "items": out.Items}, nil
				}
				return map[string]string{"status": cliStatus, "wait_id": out.WaitID}, nil
			})
		},
	}
	c.Flags().StringVar(&condKind, "condition", "", "reply_to|reply_to_all|addressed_message|task_result|board_key|stage_status (required)")
	c.Flags().StringVar(&messageID, "message-id", "", "for condition=reply_to")
	c.Flags().StringVar(&messageIDs, "message-ids", "", "for condition=reply_to_all (or reply_to): comma-separated ids of the requests to wait for (1-16, no duplicates)")
	c.Flags().StringVar(&threadID, "thread-id", "", "for condition=addressed_message; omit to wait for a new message to you in any thread")
	c.Flags().StringVar(&taskID, "task-id", "", "for condition=task_result")
	c.Flags().StringVar(&stageID, "stage-id", "", "for condition=stage_status")
	c.Flags().StringVar(&key, "key", "", "for condition=board_key")
	c.Flags().StringVar(&status, "status", "", "for condition=board_key or stage_status")
	c.Flags().StringVar(&note, "note", "", "continuation note shown when this participant resumes")
	c.Flags().Int64Var(&deadlineSeconds, "deadline-seconds", 0, "optional deadline, seconds from now (0 = no deadline); one deadline covers a whole reply_to_all set")
	return c
}

// batchMessageIDs reads --message-ids of a reply_to_all wait: a comma-separated list, spaces around an id
// dropped, with no empty entry and no id twice. It names the entry at fault, so the agent can fix the call.
func batchMessageIDs(messageID, csv string) ([]string, error) {
	invalid := func(format string, args ...any) error { return newAgentCLIError("invalid_request", format, args...) }
	if messageID != "" {
		return nil, invalid("a batch wait takes --message-ids, not --message-id")
	}
	if strings.TrimSpace(csv) == "" {
		return nil, invalid("condition %s needs --message-ids (comma-separated request ids)", agentWaitReplyToAll)
	}
	parts := strings.Split(csv, ",")
	ids := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for i, part := range parts {
		id := strings.TrimSpace(part)
		switch {
		case id == "":
			return nil, invalid("--message-ids has an empty entry at position %d", i+1)
		case seen[id]:
			return nil, invalid("message id %s is listed twice in --message-ids", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

// newAgentWaitStatusCmd registers `afm agent wait-status`: how the waits of this participant stand now — one
// object per batch with its overall status (pending|ready|expired) and, per request, the wait, its status and
// the reason it ended without an answer. --wait-id opens one batch, or one single wait as a group of one;
// without it every batch of this participant is listed. A batch or wait of someone else is never shown.
func newAgentWaitStatusCmd() *cobra.Command {
	var waitID string
	c := &cobra.Command{
		Use:          agentWaitStatusName,
		Short:        "Read the outcome of each request of your batch waits (--wait-id for one batch)",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c2 *agentMeshClient) (any, error) {
				path := agentWaitStatusName
				if waitID != "" {
					path += "?wait_id=" + url.QueryEscape(waitID)
				}
				var out map[string]any
				if err := c2.do("GET", path, nil, &out); err != nil {
					return nil, err
				}
				return out, nil
			})
		},
	}
	c.Flags().StringVar(&waitID, "wait-id", "", "batch id (from `afm agent wait --condition reply_to_all`) or a single wait id; omit to list every batch of yours")
	return c
}

// newAgentSpawnCmd registers `afm agent spawn`: a stage agent starts a temporary helper of a profile for one
// instruction. It returns at once with the task, participant and message ids; the message id is the request the
// helper answers, so wait for it like for any other (`wait --condition reply_to_all --message-ids ...` for several).
// Nothing else is the agent's to choose — the stage, the command and the model follow from the caller and the profile.
func newAgentSpawnCmd() *cobra.Command {
	var profile, title, instruction string
	var requestID *string
	c := &cobra.Command{
		Use:   "spawn",
		Short: "Start a temporary helper of a profile for one instruction (stage agents only)",
		Long: "Start a one-shot advisory helper. It returns task_id, participant_id and message_id: the message is the request " +
			"the helper answers. Wait for it with `afm agent wait --condition reply_to --message-id <message_id>`, or for several helpers " +
			"at once with --condition reply_to_all --message-ids <id1,id2,...>. Read the full result with `afm agent helper-result --task-id <task_id>`.",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c2 *agentMeshClient) (any, error) {
				required := []struct{ flag, value string }{{"--profile", profile}, {"--title", title}, {"--instruction", instruction}}
				for _, r := range required {
					if strings.TrimSpace(r.value) == "" {
						return nil, newAgentCLIError("invalid_request", "%s is required", r.flag)
					}
				}
				reqBody := map[string]any{"request_id": *requestID, "profile_id": profile, "title": title, "instruction": instruction}
				var out server.MeshSpawnReceipt
				if err := c2.do("POST", "spawn", reqBody, &out); err != nil {
					return nil, err
				}
				return out, nil
			})
		},
	}
	c.Flags().StringVar(&profile, "profile", "", "profile id of the helper (required)")
	c.Flags().StringVar(&title, "title", "", "short title of the helper's task (required)")
	c.Flags().StringVar(&instruction, "instruction", "", "what the helper must do (required)")
	requestID = requestIDFlag(c)
	return c
}

// newAgentHelperResultCmd registers `afm agent helper-result`: the status of one helper you spawned and, once it
// finished, its full result (verdict, findings, evidence) — the inbox reply only summarises it. A helper that is
// still running shows its status and no result; one that failed shows status_reason.
func newAgentHelperResultCmd() *cobra.Command {
	var taskID string
	c := &cobra.Command{
		Use:          "helper-result",
		Short:        "Read the status and full result of a helper you spawned",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c2 *agentMeshClient) (any, error) {
				if strings.TrimSpace(taskID) == "" {
					return nil, newAgentCLIError("invalid_request", "--task-id is required")
				}
				var out map[string]any
				if err := c2.do("GET", "helper?task_id="+url.QueryEscape(taskID), nil, &out); err != nil {
					return nil, err
				}
				return out, nil
			})
		},
	}
	c.Flags().StringVar(&taskID, "task-id", "", "task id printed by `afm agent spawn` (required)")
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
