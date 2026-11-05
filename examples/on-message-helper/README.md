# On-message helper: one standing helper, several requests

The `explore` stage declares a helper with `trigger: on_message`. Unlike a
`one_shot` helper, it does nothing when the stage starts: in the dashboard it
shows **On message** and **Waiting for message**. The stage agent then sends it
two different requests with `afm agent send`:

1. it asks what the project's top-level files are for;
2. it asks which file to change first for a `--verbose` flag.

Each request gets its own turn and its own result, bound to the request. The
helper is the same task both times (its task id does not change), takes the
requests one at a time and replies to the sender after each one. The stage agent
registers a `reply_to` wait per request and reads each answer with
`afm agent helper-result --task-id <task_id> --message-id <message_id>`.
The helper answers with a summary only: an `on_message` helper needs no verdict.
When the stage is done, afm closes the resting helper by itself; a message sent
to it afterwards is refused.

Copy the example, including `.afm/`, into a scratch project and run it there:

```bash
mkdir /tmp/afm-on-message-demo && cp -R examples/on-message-helper/. /tmp/afm-on-message-demo/
cd /tmp/afm-on-message-demo && git init -q && git commit -q --allow-empty -m init
afm run flow.yaml
```

Open the stage in the dashboard to see the helper rest between requests, the
**Active request** and **Last request** lines, and each result labelled
**Request … · turn N**. You can also send it a message yourself (mention it in
the stage Feed): that is one more request, answered after the one in progress.

## Requirements

- The `explore` stage runs your default agent command: `client.command` from
  your global or project config, `claude` when none is set. With the default you
  need the Claude Code CLI on `PATH` and logged in (or its credentials in afm's
  Docker container). To use another agent, set `client.command` in
  `.afm/config.yaml` or `command:` on the stage.
- Collaboration on (the default) and the dashboard HTTP server on, which Agent
  Mesh needs. With `AFM_COLLABORATION=0` afm warns that the helper will not be
  attached, and the stage cannot ask it anything.
- On the host, a `codex` CLI on `PATH`: the helper uses the read-only
  `codex-as-claude` adapter. To use another agent or model, change `command` of
  the `notes` profile.

See [Helpers that wait for messages](https://akopichin.github.io/afm/collaboration/#helpers-that-wait-for-messages)
for requests, queueing, Pause, closing and required helpers.
