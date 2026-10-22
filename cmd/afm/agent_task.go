package main

import (
	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
)

// `afm agent task …` — typed board tasks over /api/agent-mesh/{tasks,task}. The CLI only maps flags onto
// server.MeshBoardTaskRequest; every rule of a change lives in the Service.

func newAgentTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Read and change typed board tasks",
	}
	cmd.AddCommand(
		newAgentTaskListCmd(), newAgentTaskGetCmd(),
		newAgentTaskChangeCmd(orchestrator.BoardTaskCreate, "Create a task, optionally assigning it at once", func(c *cobra.Command, req *server.MeshBoardTaskRequest) {
			c.Flags().String("title", "", "task title (required)")
			c.Flags().String("description", "", "task description")
			c.Flags().StringVar(&req.To, "assign-to", "", "participant (alias or id) to assign the task to at once")
			c.Flags().String("coordinator", "", "participant (alias or id) told when the task is done, released or reopened")
		}),
		newAgentTaskChangeCmd(orchestrator.BoardTaskAssign, "Assign the task to a participant", func(c *cobra.Command, req *server.MeshBoardTaskRequest) {
			c.Flags().StringVar(&req.To, "to", "", "participant (alias or id) to assign the task to (required)")
		}),
		newAgentTaskChangeCmd(orchestrator.BoardTaskClaim, "Take the task yourself and start it", nil),
		newAgentTaskChangeCmd(orchestrator.BoardTaskStart, "Start your assignment", assignmentFlag),
		newAgentTaskChangeCmd(orchestrator.BoardTaskProgress, "Report progress on your assignment", func(c *cobra.Command, req *server.MeshBoardTaskRequest) {
			assignmentFlag(c, req)
			c.Flags().StringVar(&req.Text, "text", "", "what is done so far (required)")
		}),
		newAgentTaskChangeCmd(orchestrator.BoardTaskDone, "Finish your assignment with its result", func(c *cobra.Command, req *server.MeshBoardTaskRequest) {
			assignmentFlag(c, req)
			c.Flags().StringVar(&req.Text, "summary", "", "the result (required)")
		}),
		newAgentTaskChangeCmd(orchestrator.BoardTaskRelease, "Take an assignment back from its executor (coordinator or user only)", func(c *cobra.Command, req *server.MeshBoardTaskRequest) {
			assignmentFlag(c, req)
			c.Flags().StringVar(&req.Text, "reason", "", "why the assignment is taken back")
		}),
		newAgentTaskChangeCmd(orchestrator.BoardTaskReopen, "Reopen the task, ending its assignments", func(c *cobra.Command, req *server.MeshBoardTaskRequest) {
			c.Flags().StringVar(&req.Text, "reason", "", "why the task is reopened")
		}),
		newAgentTaskChangeCmd(orchestrator.BoardTaskEdit, "Change the task's title, description or coordinator", func(c *cobra.Command, req *server.MeshBoardTaskRequest) {
			c.Flags().String("title", "", "new title")
			c.Flags().String("description", "", "new description (\"\" clears it)")
			c.Flags().String("coordinator", "", "new coordinator, alias or id (\"\" clears it: the author coordinates)")
		}),
	)
	return cmd
}

func newAgentTaskListCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "list",
		Short:        "List every board task",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c *agentMeshClient) (any, error) {
				var out map[string]any
				if err := c.do("GET", "tasks", nil, &out); err != nil {
					return nil, err
				}
				return out, nil
			})
		},
	}
}

func newAgentTaskGetCmd() *cobra.Command {
	var key string
	c := &cobra.Command{
		Use:          "get",
		Short:        "Read one board task",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c2 *agentMeshClient) (any, error) {
				var out map[string]any
				if err := c2.do("GET", withKeyQuery("tasks", key), nil, &out); err != nil {
					return nil, err
				}
				return out, nil
			})
		},
	}
	c.Flags().StringVar(&key, "key", "", "task key, task/<slug> (required)")
	return c
}

// assignmentFlag registers --assignment-id, the assignment a start, progress, done or release acts on.
func assignmentFlag(c *cobra.Command, req *server.MeshBoardTaskRequest) {
	c.Flags().StringVar(&req.AssignmentID, "assignment-id", "", "your assignment's id (required)")
}

// changedString is the value of the string flag name when it was given, nil when it was not (or the command
// has no such flag): an edit sends only the fields it changes.
func changedString(cmd *cobra.Command, name string) *string {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	value, _ := cmd.Flags().GetString(name) // a given flag of this name is always a string flag
	return &value
}

// newAgentTaskChangeCmd builds `afm agent task <action>`: --key, --request-id, --expected-version (every
// action but create), --artifacts (progress and done) and the action's own flags, which addFlags binds into
// the request.
func newAgentTaskChangeCmd(action orchestrator.BoardTaskAction, short string, addFlags func(c *cobra.Command, req *server.MeshBoardTaskRequest)) *cobra.Command {
	var req server.MeshBoardTaskRequest
	var artifacts string
	var requestID *string
	c := &cobra.Command{
		Use:          string(action),
		Short:        short,
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCmd(cmd, func(c2 *agentMeshClient) (any, error) {
				req.Action = string(action)
				req.Title = changedString(cmd, "title")
				req.Description = changedString(cmd, "description")
				req.Coordinator = changedString(cmd, "coordinator")
				if cmd.Flags().Changed("artifacts") {
					refs := append([]string{}, splitCSV(artifacts)...) // given empty, the list clears the refs
					req.ArtifactRefs = &refs
				}
				body := struct {
					RequestID string `json:"request_id"`
					server.MeshBoardTaskRequest
				}{*requestID, req}
				var out orchestrator.BoardTaskReceipt
				if err := c2.do("POST", "task", body, &out); err != nil {
					return nil, err
				}
				return out, nil
			})
		},
	}
	c.Flags().StringVar(&req.Key, "key", "", "task key, task/<slug> (required)")
	if action != orchestrator.BoardTaskCreate {
		c.Flags().IntVar(&req.ExpectedVersion, "expected-version", 0, "the task's current version (required)")
	}
	if action == orchestrator.BoardTaskProgress || action == orchestrator.BoardTaskDone {
		c.Flags().StringVar(&artifacts, "artifacts", "", "comma-separated artifact refs (\"\" clears them)")
	}
	if addFlags != nil {
		addFlags(c, &req)
	}
	requestID = requestIDFlag(c)
	return c
}
