package main

import (
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// Loop commands share the Service with the dashboard so identity and lifetime rules stay server-side.

// promptFromStdin is the --prompt-file value that reads the prompt from standard input, so a prompt holding
// a secret never enters the command line.
const promptFromStdin = "-"

func newAgentLoopCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "loop",
		Short: "Schedule a repeating prompt for yourself or a colleague; list and stop your schedules",
	}
	cmd.AddCommand(newAgentLoopAddCmd(), newAgentLoopListCmd(), newAgentLoopStopCmd())
	return cmd
}

func newAgentLoopAddCmd() *cobra.Command {
	var every, prompt, promptFile, to, untilStage string
	var requestID *string
	c := &cobra.Command{
		Use:   "add",
		Short: "Schedule a prompt to arrive every --every, to yourself unless --to names a colleague",
		Long: "Schedule a prompt that is delivered as a message every --every, starting one interval from now. " +
			"Without --to the message comes to you: end your turn and the next tick wakes you — do not register " +
			"`afm agent wait` for it. A loop aimed at a stage helper ends with that helper's stage; one aimed at a " +
			"run agent lives until the run ends. --until-stage ends it sooner, with any stage; `afm agent loop stop` ends it any time.",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c2 *agentMeshClient) (any, error) {
				text, err := loopPrompt(cmd, prompt, promptFile)
				if err != nil {
					return nil, err
				}
				if _, err := time.ParseDuration(every); err != nil {
					return nil, newAgentCLIError(codeInvalidFlags, "--every must be a duration such as 5m or 90s: %v", err)
				}
				return postLoop(c2, map[string]any{
					"request_id": *requestID, "action": "add", "target": to, "every": every,
					"prompt": text, "until_stage": untilStage,
				})
			})
		},
	}
	c.Flags().StringVar(&every, "every", "", "interval between ticks, a duration such as 5m or 90s (required)")
	c.Flags().StringVar(&prompt, "prompt", "", "the prompt delivered on each tick")
	c.Flags().StringVar(&promptFile, "prompt-file", "", "read the prompt from this file, or from stdin with - (keeps a secret out of the command line)")
	c.Flags().StringVar(&to, "to", "", "who receives the ticks: an alias (docs, @docs) or participant id; omit to schedule yourself")
	c.Flags().StringVar(&untilStage, "until-stage", "", "end the loop when this stage ends")
	requestID = requestIDFlag(c)
	return c
}

func newAgentLoopListCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "list",
		Short:        "List the loops you created, active, stopped and ended",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c *agentMeshClient) (any, error) {
				var out map[string]any
				if err := c.do("GET", "loops", nil, &out); err != nil {
					return nil, err
				}
				return out, nil
			})
		},
	}
}

func newAgentLoopStopCmd() *cobra.Command {
	var loopID string
	var requestID *string
	c := &cobra.Command{
		Use:          "stop",
		Short:        "Stop one of your loops",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c2 *agentMeshClient) (any, error) {
				if strings.TrimSpace(loopID) == "" {
					return nil, newAgentCLIError(codeInvalidFlags, "--loop-id is required (see `afm agent loop list`)")
				}
				return postLoop(c2, map[string]any{"request_id": *requestID, "action": "stop", "loop_id": loopID})
			})
		},
	}
	c.Flags().StringVar(&loopID, "loop-id", "", "id of the loop to stop, such as L1 (required)")
	requestID = requestIDFlag(c)
	return c
}

// postLoop sends one loop change and returns the loop it answered with.
func postLoop(c *agentMeshClient, body map[string]any) (any, error) {
	var out map[string]any
	if err := c.do("POST", "loop", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// codeInvalidFlags is the error code of a command line the CLI refuses before asking the server.
const codeInvalidFlags = "invalid_flags"

// loopPrompt is the prompt of `loop add`: exactly one of --prompt and --prompt-file, never blank and within
// the size a tick can carry.
func loopPrompt(cmd *cobra.Command, prompt, promptFile string) (string, error) {
	if (prompt == "") == (promptFile == "") {
		return "", newAgentCLIError(codeInvalidFlags, "give the prompt with exactly one of --prompt and --prompt-file")
	}
	text := prompt
	if promptFile != "" {
		var err error
		if text, err = readLoopPromptFile(cmd, promptFile); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(text) == "" {
		return "", newAgentCLIError(codeInvalidFlags, "the prompt is empty")
	}
	if len(text) > agentcomm.MaxLoopPromptBytes {
		return "", newAgentCLIError(codeInvalidFlags, "the prompt is longer than %d bytes", agentcomm.MaxLoopPromptBytes)
	}
	return text, nil
}

// readLoopPromptFile reads the prompt from stdin (path "-") or from a file, stopping one byte past the limit
// so an oversized input is refused without being read to its end.
func readLoopPromptFile(cmd *cobra.Command, path string) (string, error) {
	var in = cmd.InOrStdin()
	if path != promptFromStdin {
		f, err := os.Open(path)
		if err != nil {
			return "", newAgentCLIError(codeInvalidFlags, "read --prompt-file: %v", err)
		}
		defer f.Close()
		in = f
	}
	data, err := io.ReadAll(io.LimitReader(in, agentcomm.MaxLoopPromptBytes+1))
	if err != nil {
		return "", newAgentCLIError(codeInvalidFlags, "read --prompt-file: %v", err)
	}
	return string(data), nil
}
