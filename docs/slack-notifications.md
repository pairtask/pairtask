# Slack notifications

Get a Slack message when an agent needs your decision, so you don't have to
watch the chat.

```text
pairtask · office-mac: codex-1 needs a decision — merge now or wait for the release. Answer in pairtask chat (reply to d-3f9c…). Delivered is not approved.
```

You still answer in the pairtask chat. Slack only points you there: nothing
you write in Slack reaches pairtask, and a notice is never an approval.

## Before you start

- A running pairtask sandbox (see the [README](../README.md#set-up-a-sandbox)).
- The pairing route on: `pairtask pairing on`. The sandbox broker has no
  network of its own and reaches Slack through it.
- Permission to add an app to your Slack workspace.

## Set it up

### 1. Create a webhook in Slack

Slack's own guide: [Sending messages using incoming webhooks](https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/).

1. [Create a Slack app](https://api.slack.com/apps?new_app=1): give it a name
   (for example `pairtask`), pick your workspace, click **Create**.
2. In the app's settings, open **Incoming Webhooks** and switch on
   **Activate Incoming Webhooks**.
3. Click **Add New Webhook to Workspace**, choose the channel the notices
   should go to, and click **Authorize**. For a private channel, join it
   first.
4. Copy the URL under **Webhook URLs for Your Workspace**. It looks like
   `https://hooks.slack.com/services/T…/B…/…`.

> [!WARNING]
> The webhook URL is a secret: anyone who has it can post to that channel.
> Don't paste it into chats, docs or Git. Slack revokes URLs it finds leaked.

### 2. Turn notifications on

```sh
pairtask notify on --slack
```

Paste the URL at the prompt; it is not shown while you type. pairtask stores
it in the sandbox for the broker alone and restarts the broker.

Prefer a file? Put the URL in a file only you can read (`chmod 600` if the
file already exists) and pass it:

```sh
(umask 077; pbpaste > ~/.pairtask-slack-webhook)   # macOS; or write the file another way
pairtask notify on --slack --secret-file ~/.pairtask-slack-webhook
```

### 3. Check it

```sh
pairtask notify status
```

```text
Notifications: slack, secret stored
```

### 4. Try it

Ask one of your agents in the chat:

```text
@codex-1 use request_decision to ask me whether to run the release today
```

A line appears in your Slack channel, and the question appears in the chat as
a message from the agent. Reply to that message in the chat to answer it.

## Options

| You want | Run |
|---|---|
| Notices that don't say who asks or what | `pairtask notify on --slack --hidden` |
| See the settings (never the URL) | `pairtask notify status` |
| Turn it off and delete the URL | `pairtask notify off` |

With `--hidden`, Slack only gets the workspace name and "a decision is
waiting".

## What leaves your machine

One notice per request goes to `hooks.slack.com`, retried twice on a
temporary delivery failure: the workspace name, the agent's name, the
one-line summary the agent wrote, and the ID of the chat message to answer.
With `--hidden`, only the workspace name. The webhook URL never appears in
logs, command lines or the database.

## How agents use it

Agents get a `request_decision` tool ([MCP tools](mcp.md#messaging-tools)).
They call it with a one-line summary and, ideally, a stable key: asking
again with the same key returns the same request instead of asking twice.
The first answer says `pending`, because the notice goes out right after;
asking again with the same key later shows `sent` or `failed` once delivery
finishes. It says `off` when notifications are off. It never says what you
decided.

## If something goes wrong

| What you see | What to do |
|---|---|
| `the pairing route is off` | Run `pairtask pairing on`, then `pairtask notify on --slack` again. |
| `the secret must be a Slack incoming webhook URL` | Copy the full URL from step 1.4; only `https://hooks.slack.com/services/…` works. |
| No Slack message, but the question is in the chat | Slack may be unreachable or the URL revoked. pairtask retries twice (after 2 s and 8 s); the question stays open in the chat either way. Create a new webhook and run `pairtask notify on --slack` again. |
| `Notifications: off` | Run `pairtask notify on --slack`. |

## Without the sandbox

A broker you run yourself reads environment variables instead and reaches
Slack directly:

| Variable | Meaning |
|---|---|
| `PAIRTASK_NOTIFY=slack` | Turn notifications on (`off` or unset: off). |
| `PAIRTASK_NOTIFY_SECRET_FILE=/path` | File with the webhook URL, owned by the broker user and readable by it only. |
| `PAIRTASK_NOTIFY_HIDDEN=1` | Only the workspace name and "a decision is waiting". |
| `PAIRTASK_NOTIFY_WORKSPACE=name` | Name shown in the notice (default: the pairing name). |
| `PAIRTASK_NOTIFY_TIMEOUT=10s` | How long one post may take, up to `1m`. |

A missing or empty file leaves notifications off and the broker log says
why. A file others can read, or a URL of another shape, stops the broker.
