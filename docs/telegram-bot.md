# Telegram bot

Use the standalone Telegram bot to follow and control an AFM run from a private
chat. The bot connects to an **already running** AFM dashboard on the same host.
It does not start a run or register one automatically.

## Start and connect

1. Create a bot with **@BotFather** in Telegram and keep its token private. Send
   your new bot a message in a private chat. Before starting the bot, obtain your
   numeric Telegram user ID from the `from.id` field in the Bot API's
   `getUpdates` response. For example, after putting the BotFather token in a
   shell variable without echoing it:

   ```bash
   read -r -s -p 'Bot token: ' TELEGRAM_BOT_TOKEN; echo
   export TELEGRAM_BOT_TOKEN
   printf 'url = "https://api.telegram.org/bot%s/getUpdates"\nrequest = "POST"\n' "$TELEGRAM_BOT_TOKEN" | curl --silent --show-error --config -
   ```

   Check the `from.id` of **your** message, especially if the response contains
   several updates. Run this before the bot starts polling; Telegram updates may
   already have been consumed once it is running.

2. Start AFM with its dashboard in one terminal. The example uses port `9876`:

   ```bash
   afm run --port 9876
   ```

3. From an AFM source checkout, start the bot in another terminal on the same
   host. Replace `123456789` with your own numeric user ID; separate multiple
   allowed IDs with commas.

   ```bash
   cd telegram-bot
   export TELEGRAM_ALLOWED_USER_IDS=123456789
   read -r -s -p 'Bot token: ' TELEGRAM_BOT_TOKEN; echo
   export TELEGRAM_BOT_TOKEN
   go run .
   ```

   The bot requires both environment variables. It ignores group chats and
   messages from IDs outside the allowlist. It listens on `127.0.0.1:8080` for
   local registration and health checks. `GET http://127.0.0.1:8080/healthz`
   returns `ok` while the listener is up.

4. In the bot's private chat, send `/connect 9876`. The bot confirms the AFM run
   and selects it. The dashboard port is distinct from the bot's port. If AFM
   uses another dashboard port, use that port in `/connect`.

Alternatively, a local process can register the dashboard without a Telegram
command:

```bash
curl --fail-with-body --request POST http://127.0.0.1:8080/v1/runs/register \
  --header 'Content-Type: application/json' \
  --data '{"dashboard_port":9876}'
```

The endpoint accepts requests from loopback only, requires JSON, and returns a
`run_key`, `label`, and `dashboard_port`. Registration does not select a run in a
Telegram user's chat; use `/runs` to select it. The bot's `--port` flag changes
its local listener (default `8080`); `--store` changes its durable state file
(default `~/.afm/telegram-bot/state.json`). For example, run `go run . --port
8081 --store /path/to/bot-state.json` from `telegram-bot/` and change the local
registration URL accordingly. Use one bot process per state file.

## Work from Telegram

| Command | What it opens |
|---|---|
| `/start`, `/status` | Selected run and its current stage |
| `/connect PORT` | Register and select a local dashboard |
| `/runs` | Connected runs; select another run with its button |
| `/agents` | Participants and recipient selection for the selected run |
| `/inbox` | Items needing attention; switch between **Important** and **All conversations** |
| `/settings`, `/help` | Short command reminders |
| `/cancel` | Discard the current text entry |

Use the buttons shown under each card to open a conversation, reply to a
message, or select several recipients before composing. You can also mention
reachable agents by `@alias` in a message. A stage's **Plan and actions** card
shows numbered parts of the plan; its buttons approve the current plan or
request a revision. A revision needs a separate confirmation after you enter
feedback. A question card offers the available answers and, when AFM allows it,
a custom answer. Stage controls offer **Pause** and **Continue** when that stage
is eligible. **Add agent** uses an available AFM assistant profile and asks for
an instruction; a first message is optional. These actions require the
corresponding AFM capability and an active run.

After you send a message, the receipt distinguishes **Queued** (saved locally,
AFM has not accepted it), **Accepted** (AFM accepted it), **Replied** (the agent
answered), **Failed** (not delivered), and **Unknown** (the result is being
checked). Recipient receipts may differ in a multi-recipient conversation.
The bot stores outbound requests and retries eligible sends with the same
request ID after an uncertain result. For a question or stage action whose
outcome is uncertain, check the later bot notice and AFM history instead of
repeating it automatically.

## Connection and safety limits

The bot keeps a local binding to the dashboard port and the run's `started_at`
value, with the AFM run ID when supplied. If a different run takes over that
port, the old binding is invalidated; use `/connect PORT` again. A temporarily
unreachable dashboard appears **Offline**, which is not proof that the run
finished. Completed runs remain readable when the dashboard is available, but
write controls are disabled.

Before sending an action, the bot re-reads the run, stage, question or plan as
appropriate. AFM's existing API has no atomic expected-run or expected-stage
condition on these writes, so this check cannot guarantee protection against a
change between the read and the write. Keep both services on the same host;
cross-host and Docker connection paths are not supported by this bot.
