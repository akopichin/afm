# Team review: a stage agent asks reviewer agents

The flow declares two [run agents](https://akopichin.github.io/afm/collaboration/#declare-a-team-in-flowyaml)
with `team:`, an architecture reviewer (`@architecture`) and a QA reviewer
(`@qa`). Their roles are collaboration profiles in `.afm/config.yaml`. The
`implement` stage writes a small Go function and then, before it finishes:

1. sends each reviewer a request with the change summary (`afm agent send`);
2. registers one `afm agent wait --condition reply_to` per request and ends its
   turn, so it waits without using a command slot;
3. is resumed by each answer, fixes the valid findings and asks again if it
   changed the code;
4. finishes after both reviewers have answered. Then the `verify:` gate runs
   `go test ./...` and a read-only Codex review.

Each reviewer gets its own request because a `reply_to` wait ends at the first
answer to its message. With one message to both reviewers, the stage could
finish before the second answer arrives.

Copy the example, including `.afm/`, into a scratch project and run it there:

```bash
mkdir /tmp/afm-team-demo && cp -R examples/agent-team-review/. /tmp/afm-team-demo/
cd /tmp/afm-team-demo && git init -q && git commit -q --allow-empty -m init
afm run flow.yaml
```

The requests and answers appear in the stage **Feed** and in the **Team** tab.
You are a member of every conversation and can answer there too; your reply
does not count as a reviewer's answer to the stage's `reply_to` wait.

## Requirements

- The `implement` stage runs your default agent command: `client.command` from
  your global or project config, `claude` when none is set. With the default you
  need the Claude Code CLI on `PATH` and logged in (or its credentials in afm's
  Docker container). To use another agent, set `client.command` in
  `.afm/config.yaml` or `command:` on the stage.
- Collaboration on (the default). With `AFM_COLLABORATION=0` afm warns that the
  team will not be added. Messaging itself stays available, but
  `afm agent list` shows no reviewers, so the stage follows the prompt's
  fallback: it implements and tests the function without a review, notes that
  in its summary, and the `verify:` gate still runs. The same fallback applies
  when only one reviewer is listed (the stage asks that one) and when messaging
  is unavailable because the dashboard HTTP server is off.
- The dashboard HTTP server on, which Agent Mesh needs.
- On the host, a `codex` CLI on `PATH`: the reviewers and the verify step use the
  read-only `codex-as-claude` adapter (see [`../verify/`](../verify/)).

To use other agents or models, change `command` of the `rev-arch` and `rev-qa`
profiles. On the host afm runs only the read-only `codex-as-claude` adapter for
run agents; in afm's Docker container any configured command or
`docker.agents` recipe works.
