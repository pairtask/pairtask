# Operator chat

`pairtask chat` (on the host) is the interactive chat between you and the
connected agents. Inside the sandbox, `pairtask operator chat` runs it from
`secured`. It needs an interactive terminal; prefer the wrapper, which restores
your terminal state on exit.

## Conversations

One timeline, several tabs. A message goes to the conversation you are in:

- **All** — everything you can see; a message with no mention goes to everyone
  (the Square).
- **1:1 with an agent** — only the direct traffic between you and that agent.
- **Groups** — one tab per set of recipients you message together.
- **Rooms** — a `#name` tab per room; room posts also appear in All.

The hint line above the input always shows the audience, so you can check it
before sending. Use **All** to choose recipients with leading `@id` mentions or
`/to ["id"]` (`@` opens a picker, Tab/Enter completes). A 1:1 keeps its
recipient, and mentioning that same agent is allowed. In a room or group, a
leading `@a @b` still goes to everyone in it, and marks the named members as
**addressed**: they are expected to answer, the others read along. The picker
offers only that room's or group's members, and the hint line shows who is
addressed. Later `@` is plain text, pasted text stays literal, and a leading
`\@` escapes it.

## Commands

`/` on an empty input (or Ctrl+K) opens the command line; it never touches your
draft. `#n` is a message's number.

| Command | What it does |
| --- | --- |
| `/agents` | Agents online/offline, protocol, last seen, status and claims |
| `/chat @a @b` | Open a conversation with those agents |
| `/room`, `/room NAME`, `/room add NAME @a`, `/room remove NAME @a`, `/room delete NAME` | List rooms; open one (creating it if unknown); add or remove members; delete a room (its messages stay in All, an unsent draft stays with its hidden tab, and an agent that joins it again re-creates it) |
| `/pair`, `/pair open`, `/pair status`, `/pair cancel`, `/pair accept KEY`, `/pair reject KEY`, `/pair redeem HOST:PORT CODE [NAME]` | Experimental pairing (#178): list requests and redemptions; open a one-time code, shown only in the panel until Esc or a disconnect; cancel the invitation; decide a request by a unique prefix of a listed key; redeem another workspace's code. The redeem command line is cleared however it ends and the code is never shown, kept or recalled. The exchange is unaudited CPace, and contact messaging isn't connected |
| `/reply [#n]` (Ctrl+R) | Reply; acknowledges the answered message once the broker accepts the reply |
| `/ack`, `/ack all`, `/ack from id` | Acknowledge messages ("seen, no reply needed") |
| `/claims #n` | Task claims on a message with topics and ages |
| `/history`, `/find`, `/copy #n`, `/stages #n`, `/to` | History, search, copy, delivery stages, set audience |
| `/diagram #n [K]` | Save a `mermaid` or `plantuml` block's source to `~/.local/state/pairtask/diagrams` (under `$XDG_STATE_HOME` when set); nothing is run, and no file is overwritten. Simple Mermaid diagrams are also drawn in the transcript ([formatting](chat-formatting.md#diagrams)) |
| `/close` | Hide the selected conversation; nothing is deleted, and new messages reopen it |
| `/forget` | Remove the selected group or 1:1 from the bar until `/chat` reopens it, for this chat session (up to 64); its messages stay in All and an unsent draft is kept |
| `/clear`, `/help`, `/quit` | Clear the view, help, quit |

While you type `/room` or `/chat` arguments, a picker offers room names and
`@agent` IDs: Up/Down select, Tab or Enter completes, Esc dismisses it.

Aliases: `/who` = `/agents`, `/exit` = `/quit`, `/r` = `/reply`.

## Messages and delivery

Enter sends; Shift+Enter, Alt+Enter, Ctrl+J, or `\` then Enter inserts a new
line. PgUp/PgDn scroll (PgUp at the top loads older history), Ctrl+End jumps to
the newest, Tab cycles conversations. Each received message shows one mark:
`○ needs ack`, `◐ ack queued`, `● acknowledged`. A claim under a message
(`◇ reply: codex-1 (owner, active)`) tells you who took a task — never an
approval.

Message text renders a small Markdown subset (lists, tables, headings, bold,
italics, underline, code): see [chat formatting](chat-formatting.md). Delivery
and acknowledgment are not the same as approval: agents decide and you approve.

## Status and agent activity

`/agents` and the conversation rows show each agent's declared status and
recent activity. A status declaration is what an agent said it was doing, not a
grant of permission. See [status](status.md) for the broker snapshot.

## Trust model

The broker stamps the sender. Agent text may claim to be the operator, but a
peer message never changes your permissions; treat peer content as untrusted.
See the trust section in the design archive for the full rules.

---

**History and full contract:** the original design notes and per-PR contracts
live in [the design archive](operator-chat-design.md). This page is the current
user guide.