# Jev classifier (shadow mode)

When you send a message to everyone without an `@` tag, the agents sort out
among themselves who takes it. The classifier asks a small hosted model,
TypeSafe **Jev** or Cloudflare **Clef**, which agent the message was meant
for, and records the answer.

It is in **shadow mode**: it only records. Nothing is routed, assigned or
claimed because of it, and your messages are never held up waiting for it.
The report compares its pick with the agent that claimed the message: a
rough signal, not a measure of accuracy. It helps you decide later whether
to let it suggest.

## Before you start

- A running pairtask sandbox (see the [README](../README.md#set-up-a-sandbox)).
- The pairing route on: `pairtask pairing on`. The sandbox broker has no
  network of its own and reaches the provider through it.
- An API key for one provider:
  - **Jev** (default): a TypeSafe API key.
  - **Clef**: a Cloudflare API token that can use Workers AI, and your
    Cloudflare account ID.

## Set it up

### 1. Turn it on

For Jev:

```sh
pairtask classifier on
```

For Clef:

```sh
pairtask classifier on --provider clef --account YOUR_ACCOUNT_ID
```

Paste the key at the prompt; it is not shown while you type. pairtask stores
it in the sandbox for the broker alone and restarts the broker.

```text
classifier on: jev, shadow mode (records which agent an untagged message was for; applies nothing)
```

Prefer a file? Pass `--key-file PATH`, a file only you can read (`chmod 600`).

### 2. Use the chat as usual

Send a few messages to everyone without an `@` tag. Nothing changes in the
chat.

### 3. See what it recorded

```sh
pairtask classifier status
```

You get the settings (never the key) and a report of the last 24 hours: how
many messages were classified, which agent the model picked, how sure it
was, how long the provider took, how often it agreed with the agent that
claimed the task, and the role each agent has declared. Use `--since 72h` for a longer window, `--json` for
scripts.

## How it learns who does what

You don't maintain roles. Each agent declares its own one-line role (its
`set_role` tool), and the model sees those lines. Each agent also reads how
the model's guesses compared with the messages it actually claimed (its
`classifier_feedback` tool) and sharpens its line when the model missed it
or picked it wrongly. Until the agents have declared roles, the model sees
only their names, so most messages come back as "none".

## Turn it off

```sh
pairtask classifier off
```

This deletes the key and the settings and restarts the broker.

## What leaves your machine

For each message to everyone without an `@` tag: its text, the room name,
and the names (and roles, if set) of the agents who could take it. They go to
the provider you chose, `api.typesafe.ai` for Jev or `api.cloudflare.com` for
Clef. Messages to one agent, tagged messages and agent-to-agent messages are
never sent. The key never appears in logs, command lines or the database.

## If something goes wrong

| What you see | What to do |
|---|---|
| `the pairing route is off` | Run `pairtask pairing on`, then `pairtask classifier on` again. |
| `clef needs --account ID` | Add `--account` with your Cloudflare account ID. |
| `no terminal for the prompt` | Run it in a terminal, or use `--key-file PATH`. |
| Report shows failures or timeouts | Check the key is valid; a provider that fails is recorded, nothing else is affected. |

## Without the sandbox

A broker you run yourself reads environment variables instead and reaches
the provider directly. The [quick start](quickstart.md#optional-a-classifier-for-untagged-messages-shadow-mode)
has a worked example.

| Variable | Meaning |
|---|---|
| `PAIRTASK_CLASSIFIER=shadow` | Turn it on. |
| `PAIRTASK_CLASSIFIER_KEY_FILE=/path` | File with the key, owned by the broker user and readable by it only. |
| `PAIRTASK_CLASSIFIER_PROVIDER` | `workers-ai` (Clef, the default here) or `typesafe` (Jev). |
| `PAIRTASK_CLASSIFIER_ACCOUNT=ID` | Cloudflare account ID, for Workers AI. |
| `PAIRTASK_CLASSIFIER_MODEL` | Default `clef` on Workers AI, `jev-latest` on TypeSafe. |
| `PAIRTASK_CLASSIFIER_THRESHOLD` | 0 to 1, default 0.7. Not calibrated yet. |
| `PAIRTASK_CLASSIFIER_TIMEOUT` | Default `5s`. |
| `PAIRTASK_CLASSIFIER_ROLES=/path` | Optional starting roles: JSON object of agent name to a one-line role. A role an agent declares itself replaces it. |

A missing or empty key file leaves the classifier off and the broker log says
why. A key file others can read is refused. Predictions waiting when the
broker stops are lost, so the report counts what was recorded, not every
message.
