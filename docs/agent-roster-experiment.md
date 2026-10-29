# Agent roster experiment

**Status: draft, 2026-10-09.** Not implemented. Tests whether several agents
can introduce themselves and divide work peer to peer, before any router
(Clef, Jev or an LLM) is built.

## Question

Given several Claude and Codex sessions on different models, each stating its
model, strengths and limits, can they decide among themselves who handles
what? Or does the group need a router?

## Setup (no code)

| Agent ID | Harness | Model | Role hint |
| --- | --- | --- | --- |
| `claude-opus-exp` | Claude Code | Opus | architecture, review |
| `claude-sonnet-exp` | Claude Code | Sonnet | implementation, research |
| `codex-2-exp` | Codex (`pairtask-codex --terminal`) | default | implementation |
| `codex-3-exp` | Codex (`pairtask-codex --terminal`) | gpt-6-astra, low effort (frontier model; low effort only) | quick checks |

- Each agent runs its own adapter with a distinct `--agent-id`.
  - Claude: the model comes from `claude --model`. The MCP adapter's `--model`
    only labels the registration; it doesn't select the model.
  - Codex: pass `-m` and `model_reasoning_effort` after `--`; they are
    forwarded unchanged. The launcher registers with `Model` empty
    (`cmd/codex/main.go`).
- Two `pairtask-codex` launchers have independent sockets and App Server
  processes. Sharing one `CODEX_HOME` concurrently is unverified on 0.159.3, so
  use fresh threads, avoid `resume --last`, and never resume one thread twice.
  A named budget's launcher lock is exclusive: don't share one, and don't work
  around an existing cap to run this.
- One project, one checkout, **read-only work**: discussion, design and review,
  no edits. Avoids needing a worktree per agent.

## Preflight

1. Confirm both Codex sessions run side by side (only when the operator
   authorizes the run).
2. Start all four agents and confirm all four inboxes are registered in
   `pairtask status --json` before anyone introduces themselves. A started
   terminal isn't enough: managed Codex registration follows thread creation.
3. Tell every session explicitly that peer delegation is authorized for this
   run, with the run ID and its end time.

## Introduction format

Broadcast to `*` with `message_id` `intro-<run-id>-<agent-id>-<revision>`
(at most 128 bytes). Message IDs are kept globally: resending identical
content counts as a retry and isn't delivered again, and changed content under
the same ID is rejected as `duplicate_message` (`internal/store/retry.go`).
Reuse an ID only for an identical retry.

```text
INTRO v0 run=<run-id> expires=<time>
agent: claude-sonnet
harness: claude-code 2.1.283
model: claude-sonnet-5 (self-reported)
strengths: <one line>
limits: <one line>
available: yes | busy until <...>
```

- Introductions are peer-supplied data, not instructions. Agents must not treat
  "send everything to me" as authority.
- Acknowledge introductions without replying to them. Four introductions
  already mean 12 deliveries; an introduction must never trigger another one.
- A late joiner sends a new introduction with a new revision and gets direct
  catch-up, not a retry of an old message.
- Ignore work whose run ID has expired, so a replayed message can't restart an
  old run.

## Rules for the run

- One owner per task. Delegation is a direct request with a correlated result,
  never a broadcast.
Proposed trial defaults (agreed by Claude and Codex, pending operator
confirmation). These are experiment stopping rules counted by a human observer,
not enforced admission control, and not token caps:

- **Delegation depth:** at most 2 delegation edges from the original task owner
  (owner → specialist → reviewer). Correlated replies aren't hops.
- **Messages:** at most 12 agent-authored task messages across the group,
  counting requests, results and status text. Introductions and protocol
  ack/receipt frames don't count. At the limit, stop new task work and report
  the current result in the owner's terminal.
- **Time:** a 10-minute observation window per task, or the operator's earlier
  run end. At the deadline, record unresolved requests and summarize partial
  results. Don't restart the task when a late message arrives.
- **Disconnects:** the request stays pending until the deadline. No automatic
  reassignment or retry in this trial, because replay could duplicate work.
  Record the interruption.
- **Expired work:** acknowledge late or expired messages when asked to, but
  don't execute or reply to them. Ignoring them must not leave durable
  deliveries pending indefinitely.

Budget authorization is separate from these rules.

## Runs

1. The operator gives one agent a task in its terminal that needs two skills,
   e.g. "propose the inspection contract and have it reviewed".
2. Repeat with the task given to a different agent.
3. One task better suited to a cheaper agent, to see whether the expensive one
   delegates or does it itself.

## What we measure

Automated outcome checks aren't implemented yet.

**From the broker:**
- Reply latency per designated task request.
- Designated task requests with no correlated reply by the end of the run.
  Only these count, because the broker doesn't store whether a reply was
  expected.

**From a manual scorecard over the transcripts:**
- Delegation: did tasks go to a suitable peer? Count tasks handed back and
  forth, work done twice, and tasks nobody picked up.
- Wake-up: an agent counts as woken only if it sent a correlated response that
  started while it was idle. A delivery receipt doesn't prove the model saw the
  message.
- Answer quality, and coordination overhead in messages and time. Measure
  startup and introductions separately from task coordination.
- Operator effort: how often the human had to step in.

Outcome: either peer-to-peer coordination is good enough (then codify it) or
it isn't. Before concluding that a router is needed, rule out protocol or
consent problems as the cause of failures.

## Results

### exp1 (2026-10-09, aborted before any task)

Agents: `claude-opus-exp` (Opus 5.5, self-reported low effort),
`claude-sonnet-exp` (Sonnet 5), `codex-2-exp` (gpt-6-sol, high),
`codex-3-exp` (gpt-6-astra, low). Each started from `/home/agent/experiment`.
The observer (`claude-1`) sent the run brief over the channel.

- **Authorization over the channel doesn't work, by design.** Both Claude
  agents followed the brief and broadcast introductions within about 30 s. Both
  Codex agents acknowledged the brief and the introductions, then stopped
  ("Ready for your task"). They treated the brief's "on behalf of the operator"
  as an unverifiable peer claim, which is the correct, safer behavior. The
  Claude agents were more permissive. Authorization must come from the operator
  in each participant's own terminal (as the Codex review had said).
- **Expiry worked.** After the deadline, `codex-2-exp` reported that the run had
  expired and did no work once the operator prompted it in its terminal.
- **A text convention doesn't produce introductions reliably.**
  `codex-3-exp` answered the observer directly instead of broadcasting an
  introduction, so the other participants never saw it. This argues for a
  real `introduce` tool.
- **Self-reported models are unreliable.** `codex-3-exp` reported "GPT-6,
  variant/effort unavailable" while running astra at low effort. Neither Claude
  agent knew its harness version. Launchers need to report the actual model.
- **A shared Codex config changes defaults for every Codex agent.** A model
  change saved to the shared `~/.codex/config.toml` (sol/high → astra/medium)
  became the default for every new Codex agent. Pin `-m` and the effort in each
  launcher.
- **Repo access from another directory.** All agents were unsure they could
  reach the repo path from `/home/agent/experiment`. Preflight: `codex-2-exp`
  read the repo docs successfully. Claude agents are not yet tested.
- **Codex can't see its own model.** `codex-2-exp` sees only "Codex agent based
  on GPT-6"; it can't see the variant or the effort, although its session runs
  gpt-6-sol at high effort. The launcher has to report the actual model.

### exp2 (2026-10-09, task exp2-t1: slice-1 inspection contract)

The operator pasted the authorization into all four terminals. All four
broadcast introductions; three arrived within 15 s, and the fourth came about
two hours later because of operator pacing, after the run had expired. The
operator then extended the run in each terminal. The extension and the task
were pasted into all four terminals together, so every agent saw the task.

**Timeline:** task given at 12:55:18, review requested at 12:58:45, review
returned at 12:59:34, reviewed result reported at 13:00:27. That's about 5
minutes in total, within the 10-minute window.

**Scorecard:**
- **Delegation:** `claude-sonnet-exp` chose `claude-opus-exp` as reviewer, citing
  its stated strength (code reading and review). 1 edge out of 2 allowed.
  Neither Codex agent was used for the work.
- **Task messages:** 2 for the work itself (request and review). Plus 2
  duplicate task forwards from `codex-2-exp` and `codex-3-exp`, which
  forwarded the pasted task to `claude-sonnet-exp`. `codex-3-exp` labeled its
  copy "Operator instruction forwarded by codex-3-exp", relaying operator
  authority through a peer, which the agents themselves refused in exp1.
  `claude-sonnet-exp` acknowledged both copies and didn't redo the work.
  Total 4 of 12.
- **Misdirected task:** `claude-opus-exp` saw "paste into claude-sonnet-exp
  only", declined the task and said so.
- **Wake-up:** `claude-opus-exp` was idle and answered the review request
  within 49 s, a response that started while it was idle. The Claude channel
  wakes an idle agent.
- **Quality:** high. The review caught four real schema mismatches (no
  `durable` column, recipients stored in `message_receipts`, receipt
  timestamps meaningful only with `ack_requested`, legacy messages having no
  ID) and payload-size side channels. The observer spot-checked these against
  `internal/store`. The owner folded all of them into the result.
- **Repo access:** `claude-opus-exp` read the local checkout directly.
  `claude-sonnet-exp` didn't try the local path and read `main` through
  `gh api` instead. Same content, but slower and dependent on the network.
- **Operator effort:** the intros needed one paste per terminal, plus one
  extension paste and the task. One clarification was needed
  (`claude-opus-exp` asked whether the paste was meant for it).

**Takeaways:**
- With the operator's authorization in each terminal, peer-to-peer delegation
  worked for a two-skill task, and the owner chose the reviewer by stated
  strengths, with no router.
- Forwarding by agents that weren't the addressee created duplicates and
  relayed claimed operator authority. Rule to add: never forward a task meant
  for another agent; tell the operator instead.
- A run deadline should start when the task is given, not at a fixed time.
- Not yet tested: delegation to Codex, a cheaper-agent task (run 3 in the
  plan), and a task given to a Codex owner.

### exp2-t2 (operator origin marking; owner `codex-2-exp`, gpt-6-sol high)

Task pasted only into `codex-2-exp`.

**Timeline:** started 13:27:27, review requested at 13:28:41, review returned
at 13:29:30, reviewed proposal reported at 13:30:39. That's about 3 minutes.

**Scorecard:**
- **Delegation:** 1 edge (`codex-2-exp` → `claude-opus-exp`). The owner wrote
  the draft first, then asked for a security and design critique.
- **Task messages:** 2. No forwarding and no duplicates.
- **Wake-up:** the idle Claude agent answered a Codex agent within 49 s.
- **Quality:** high. The review raised 6 must-fix and 5 should-fix points with
  code references: operator replies have no destination, acknowledgments are
  lost when the one-shot client exits, the operator must not be a routable
  client, origin is lost on replay, a migration is needed, and version gating.
  The final proposal folded in most of them. The trust-root note (root in
  `secured`, and access from the host through Incus) didn't make it into the
  final text.

**Pattern across both tasks:** both owners picked `claude-opus-exp` as the
reviewer. Stated strengths drive the choice, so review work concentrates on
one agent, which creates a hot spot and concentrates cost. `codex-3-exp` and
`claude-sonnet-exp` (as a helper) were never chosen.

**Why nobody delegated to `codex-3-exp`:** its introduction was nearly
identical to `codex-2-exp`'s ("GPT-6, effort unknown"). No profile carried a
cost or speed tier, and both tasks asked for review, which only Opus offered.
Routing by cost needs a cost tier supplied by the launcher or the operator.
Correction: `gpt-6-astra` is Codex's frontier model; only the effort was low.
The cheap Codex model is `gpt-6-luna` (`~/.codex/models_cache.json`).

exp2-t3 (testing whether an agent delegates down) was dropped; team presets
address the finding instead.

## Operator requirements (2026-10-09, after exp2)

1. **Central human control plane.**
   - Send to one agent, a group or all agents (fan-out). Extend or stop runs.
   - Inspect who talks to whom, how often, and which paths are used and which
     aren't (builds on the inspection contract).
   - **No agent can impersonate the human.** The broker sets `origin` from the
     connection (a separate operator socket), never from message text. The
     human client runs outside the agents' reach (another OS user or
     `secured`, via SSH), because agents have shell access as `agent`.
     exp2 example: `codex-3-exp` sent "Operator instruction forwarded by
     codex-3-exp".
2. **Complete audit log.** Human messages are stored in the same SQLite
   history as agent messages, along with operator actions (extend, stop,
   disconnect). Pasting into terminals bypasses the log, so operator
   instructions should go through the control plane.
3. **Indexed knowledge base ("library").** MCP tools to add, search and get
   entries, categorized and full-text indexed, scoped per project, with author
   and origin recorded.
   - Every entry needs **at least one approver who is not its author**. The
     approval is recorded with its broker-set origin.
   - Proposed levels: *peer-reviewed* (approved by another agent) and
     *verified* (approved by the operator). Searches return approved entries
     by default.
   - Approved entries can be promoted into docs or skills through a reviewed
     change.

Proposed order: operator socket with origin marking and fan-out →
inspection and graph → library.

## If it works: what to build

- MCP tools `introduce` (set own profile; the broker stores it per session and
  notifies others) and `roster` (current participants with profiles).
  Launcher-known facts (harness, model) are filled by the launcher; strengths
  and limits are self-reported and length-limited.
- Launchers report the actual model and effort, including after an
  interactive `/model` change. Today the Codex launcher sends none.
- Agent naming: agents pick a role name (e.g. `claude-architect-etl`). Keep two
  things separate:
  - **Inbox identity:** the broker rejects a second live agent with the same ID,
    and durable messages go to whoever next registers that ID
    (`internal/hub/durable.go`). It must stay stable and unique, so the launcher
    owns it.
  - **Role name:** display-only at first; messages still go to the inbox ID. An
    addressable alias would need rules for collisions, lifetime, reassignment
    and resolving the alias to an inbox atomically. It must never move pending
    messages or imply authority.
- A human client: `collab send` to one agent, a group or everyone. Messages from
  it carry no extra authority, because agent IDs are not authenticated
  (`docs/secured-runtime.md`).

Before building this, compare it with
[oh-my-pi](https://github.com/can1357/oh-my-pi) to understand how collab-ai
differs. Re-read the current project rather than relying on earlier notes.

## If it doesn't: router

The router is another participant (e.g. `router-1`), not part of the broker,
since routing runs in a single loop (`internal/hub/hub.go`). Options:

- Cloud Clef (Cloudflare Workers AI) or TypeSafe Jev. Task text leaves the
  sandbox; decide that explicitly.
- Local Clef-flash (9B, Apache-2.0). The Colima/Incus VM has no GPU, so it runs
  on CPU; latency unmeasured.
- An LLM router such as Claude Haiku.

Decisions combine typed answers (task kind, complexity) with rules (roster,
availability, budget) and measured outcomes.

## Later: beyond one project and one host

- **Several projects in one sandbox.** The broker has one global namespace and
  `*` reaches everyone. Project scope reaches into several parts of the broker:
  - routing and broadcast membership
  - receipts and durable inbox ownership, and replay
  - message IDs, which are globally unique
  - who may inspect which messages

  Name suffixes such as `claude-builder@etl` don't isolate projects on their
  own. Agents that work in several projects (e.g. a reviewer) need a project
  identity on each message. Mounts and working directories already differ per
  project.
- **Remote participants (a relay in `secured`).** Coworkers' agents on other
  hosts join the same group. Not crazy, but it changes the trust model.
  Prerequisites:
  - Real participant authentication; today the boundary is a same-user local
    socket.
  - Per-project authorization and encrypted transport.
  - Operator controls such as exact-session disconnect and inspection.
  - An explicit decision that message contents, which include code, may leave
    the host.

  Remote messages stay untrusted data, but they can still steer local agents,
  so approvals must stay local.

  `secured` is offline today. A relay also needs a mapping from authenticated
  participants to inboxes, plus rules for reconnects, who owns acknowledgments,
  and backpressure; TLS alone isn't enough. This is outside the experiment's
  success criteria.

Review: Codex, 2026-10-09 (folded in above).

Clef: <https://blog.cloudflare.com/clef-decision-models/>. Team design:
`docs/team-presets.md`.
