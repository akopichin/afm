package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
)

// A stage agent talks to the user through these two commands instead of hand-written question/answer files:
// the command picks the question id and file name, validates the payload and reports a mistake right where
// the agent can fix it. The files stay the storage the dashboard poller reads.

// Exit codes of `afm stage wait-answer`.
const stageExitWaiting = 75 // EX_TEMPFAIL: no answer yet, run the same command again.

const (
	stageWaitDefaultSeconds = 100 // below the shortest Bash tool timeout agents are told to use
	stageWaitMinSeconds     = 1
	stageWaitMaxSeconds     = 600
)

// stageWaitInterval is how often wait-answer looks for the answer file.
var stageWaitInterval = time.Second

// stageCLIError is a failure printed as {"error","message"} on stderr with exit code 1.
type stageCLIError struct{ code, message string }

func (e *stageCLIError) Error() string { return e.code + ": " + e.message }

// newStageCmd registers `afm stage`, hidden: it is meant for the agent, not for a human.
func newStageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "stage",
		Short:  "Stage agent commands: ask the user a question and wait for the answer (internal)",
		Hidden: true,
	}
	cmd.AddCommand(newStageAskCmd(), newStageWaitAnswerCmd())
	// A wrong call is a mistake the agent fixes like any other, so it gets the same JSON error, never usage text.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return printStageError(c, &stageCLIError{"invalid_arguments", err.Error()})
	})
	return cmd
}

// stageArgs reports an argument mistake the way SetFlagErrorFunc reports a flag mistake.
func stageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return printStageError(cmd, &stageCLIError{"invalid_arguments", err.Error()})
		}
		return nil
	}
}

func newStageAskCmd() *cobra.Command {
	var options []string
	var allowCustom bool
	cmd := &cobra.Command{
		Use:   "ask",
		Short: "Ask the user a question (text from stdin) and print its id",
		Long: "Reads the question text (markdown) from stdin and creates the next question of the current phase.\n" +
			"Repeating the same question returns the same id. Prints {\"id\",\"wait\"}; then run the printed wait command.\n" +
			"On a mistake exits 1 with {\"error\",\"message\"} on stderr naming the field: fix the call and run it again.",
		SilenceUsage: true, SilenceErrors: true,
		Args: stageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStageCmd(cmd, func() (any, error) {
				stageDir, phase, err := stageAuthorEnv()
				if err != nil {
					return nil, err
				}
				text, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), mcp.MaxQuestionBytes+1))
				if err != nil {
					return nil, &stageCLIError{"io_error", "read question text from stdin: " + err.Error()}
				}
				if len(text) > mcp.MaxQuestionBytes {
					return nil, &stageCLIError{mcp.AskCodeInvalidQuestion, fmt.Sprintf("question: text is larger than %d bytes", mcp.MaxQuestionBytes)}
				}
				res, err := mcp.AskQuestion(stageDir, phase, mcp.QuestionPayload{
					Question: string(text), Options: options, AllowCustom: &allowCustom,
				})
				if err != nil {
					var askErr *mcp.AskError
					if errors.As(err, &askErr) {
						return nil, &stageCLIError{askErr.Code, askErr.Message}
					}
					return nil, err
				}
				return map[string]string{"id": res.ID, "wait": waitAnswerCommand(res.ID)}, nil
			})
		},
	}
	cmd.Flags().StringArrayVar(&options, "option", nil, "answer option; repeat for several")
	cmd.Flags().BoolVar(&allowCustom, "allow-custom", true, "let the user type an answer other than the options")
	return cmd
}

func newStageWaitAnswerCmd() *cobra.Command {
	var timeoutSeconds int
	cmd := &cobra.Command{
		Use:   "wait-answer [qN]",
		Short: "Wait for the user's answer to a question (default: the latest question)",
		Long: "Blocks until the user answers, up to --timeout seconds (default: $AFM_WAIT_ANSWER_TIMEOUT or 100, 1-600).\n" +
			"Exit 0 prints {\"id\",\"answer\",\"from_options\"}. Exit 75 prints {\"status\":\"waiting\",...}: no answer yet,\n" +
			"run the same command again. Exit 1 prints {\"error\",\"message\"} on stderr. Repeating the command is always safe.",
		SilenceUsage: true, SilenceErrors: true,
		Args: stageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			stageDir, phase, err := stageAuthorEnv()
			var id string
			if err == nil {
				id, err = resolveQuestionID(stageDir, phase, args)
			}
			if err != nil {
				return printStageError(cmd, err)
			}
			answer, ok, err := mcp.WaitForAnswer(cmd.Context(), stageDir, phase, id, waitAnswerTimeout(timeoutSeconds), stageWaitInterval)
			switch {
			case err != nil:
				return printStageError(cmd, &stageCLIError{"interrupted", err.Error()})
			case ok:
				return printStageJSON(cmd, map[string]any{"id": answer.ID, "answer": answer.Answer, "from_options": answer.FromOptions})
			}
			if err := printStageJSON(cmd, map[string]string{"status": "waiting", "id": id, "retry": waitAnswerCommand(id)}); err != nil {
				return err
			}
			return &ExitError{Code: stageExitWaiting, Silent: true}
		},
	}
	cmd.Flags().IntVar(&timeoutSeconds, "timeout", 0, "seconds to wait before exit 75 (0: the default)")
	return cmd
}

// resolveQuestionID takes the id from the argument, else the phase's latest question, and checks it exists.
func resolveQuestionID(stageDir, phase string, args []string) (string, error) {
	if len(args) == 0 {
		id, ok := mcp.LastQuestionID(stageDir, phase)
		if !ok {
			return "", &stageCLIError{"no_question", "this phase has no question yet: run `afm stage ask` first"}
		}
		return id, nil
	}
	id := args[0]
	if !mcp.ValidQuestionID(id) {
		return "", &stageCLIError{"invalid_question_id", fmt.Sprintf("%q is not a question id like q1", id)}
	}
	if _, err := os.Stat(filepath.Join(stageDir, phase+"."+id+".question.json")); err != nil {
		return "", &stageCLIError{"no_question", fmt.Sprintf("question %s does not exist in this phase", id)}
	}
	return id, nil
}

// stageAuthorEnv returns the stage directory and phase of the stage author's run. Verify, helpers and
// anything outside a run lack AFM_DIALOG_PHASE and so cannot ask questions.
func stageAuthorEnv() (stageDir, phase string, err error) {
	stageDir, phase = os.Getenv("AFM_STAGE_DIR"), os.Getenv("AFM_DIALOG_PHASE")
	switch {
	case !filepath.IsAbs(stageDir):
		return "", "", &stageCLIError{"not_stage_author", "AFM_STAGE_DIR is not set to an absolute path: only a stage agent run by afm can ask questions"}
	case !flow.IsValidPhase(phase):
		return "", "", &stageCLIError{"not_stage_author", fmt.Sprintf("AFM_DIALOG_PHASE=%q is not a dialog phase: only a stage agent run by afm can ask questions", phase)}
	}
	return stageDir, phase, nil
}

// waitAnswerTimeout picks the wait limit: the flag, else AFM_WAIT_ANSWER_TIMEOUT, else the default, always clamped.
func waitAnswerTimeout(flagSeconds int) time.Duration {
	seconds := stageWaitDefaultSeconds
	if env, err := strconv.Atoi(os.Getenv("AFM_WAIT_ANSWER_TIMEOUT")); err == nil {
		seconds = env
	}
	if flagSeconds != 0 {
		seconds = flagSeconds
	}
	return time.Duration(min(max(seconds, stageWaitMinSeconds), stageWaitMaxSeconds)) * time.Second
}

func waitAnswerCommand(id string) string { return "afm stage wait-answer " + id }

// runStageCmd renders fn's result as one JSON object on stdout, or its error as {"error","message"} on stderr.
func runStageCmd(cmd *cobra.Command, fn func() (any, error)) error {
	result, err := fn()
	if err != nil {
		return printStageError(cmd, err)
	}
	return printStageJSON(cmd, result)
}

func printStageJSON(cmd *cobra.Command, v any) error {
	return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
}

func printStageError(cmd *cobra.Command, err error) error {
	var ce *stageCLIError
	if !errors.As(err, &ce) {
		ce = &stageCLIError{"internal", err.Error()}
	}
	_ = json.NewEncoder(cmd.ErrOrStderr()).Encode(map[string]string{"error": ce.code, "message": ce.message})
	return &ExitError{Code: 1, Silent: true}
}
