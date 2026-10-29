# Team presets and collaboration workflow (draft)

**Status: draft, reviewed by Codex 2026-10-09; updated to the duo model
2026-10-12.** Not implemented. Builds on the roster experiment
(`docs/agent-roster-experiment.md`) and `docs/runtime-boundaries-v2.md`.

## Principles

1. **The duo is the working unit, always.** A duo is two agents from two
   vendors: a Claude agent (Fable, Opus or Sonnet) and a Codex agent (Astra or
   Sol). Every task is owned by exactly one duo. No design, implementation or
   review rests on one model.
2. **Teams are multiples of duos.** A team is N duos plus a **forum**: you and
   all duos, where tasks are discussed and allocated to duos. Larger teams add
   duos, not solo specialists.
3. **The human makes the final decision.** Within a duo, both agents converge,
   record where they agree and disagree, and the human decides. Design
   decisions that affect more than one task are made in the forum with the
   human, not inside one duo.
4. **Roles inside a duo follow operator experience.** In the operator's
   experience, Codex is the strongest reviewer and the strictest about following
   rules, while Claude is stronger on open-ended design. This is attributed to
   that experience, not a benchmark result.
5. **Cost is tuned per duo, not by splitting roles.** Each duo has a tier.
   Routine tasks go to a cheaper duo, hard ones to a richer duo.

## The forum

- The human, plus one representative per duo or all duo members, depending on
  the team's charter. It runs over the operator control plane: the human
  fans out to the team, duos reply in the forum.
- The forum discusses work, splits it into tasks, and **allocates each task to
  one duo**. The duo then runs the workflow below on its own task.
- Cross-task design decisions (interfaces between tasks, shared schemas,
  architecture) are recorded in the forum and decided by the human.
- Allocation is a recorded forum decision: task ID, owning duo, scope, and
  dependencies on other tasks.

## Workflow inside a duo (the proven pattern)

| # | Step | Actor |
| --- | --- | --- |
| 1 | Discuss the allocated task (with the human, or from the forum's decision) | Claude agent |
| 2 | Design validation, several rounds | Claude ↔ Codex |
| 3 | Write acceptance tests | Codex agent |
| 4 | Build | Claude agent |
| 5 | Review; may add regression tests | Codex agent |
| 6 | Follow-ups and fixes | Claude agent |
| 7 | Create the PR | Claude agent |
| 8 | Run the test suite | CI |
| 9 | Validate HEAD: `MERGE <sha>` or `BLOCKED <sha>: <finding>` | Codex agent |
| 10 | Merge, or hand the merge to the human | Claude agent, per human policy |

- **Gates:**
  - Step 4 doesn't start before the human has decided on step 2's design
    record. For small tasks, the human may decide that the forum's allocation
    already covers it.
  - Step 10 requires `MERGE` on the PR's current remote HEAD, the required CI
    results, and the repo's other gates (CodeScene, Copilot review). Any later
    change invalidates the verdict.
- **Design record (step 2):** each agent drafts independently, then they
  exchange critiques, then they write one record: agreements, open
  disagreements with each side's position, and the decision requested from the
  human.
- **Merge policy** is set per team by the human: the human merges, or the
  Claude agent merges after `MERGE` and green CI. `MERGE <sha>` is review
  evidence, never permission to merge. The verdict records the repo/PR, exact
  HEAD, the CI results and the merge policy.
- **Between duos:** duos branch from the reviewed baseline of any task they
  depend on. A duo never weakens another duo's acceptance tests; it proposes
  changes in the forum. Integration across tasks is a forum-allocated task of
  its own when needed.

## Presets (MVP: duo and team-4; team-6 later)

**Duo tiers:**

| Tier | Claude agent | Codex agent |
| --- | --- | --- |
| rich | Fable | `gpt-6-astra` (high) |
| moderate | Opus (medium) | `gpt-6-astra` (medium) or `gpt-6.1-sol` (high) |
| low | Opus (medium/low) or Sonnet | `gpt-6.1-sol` (medium) or `gpt-6-luna` (narrow tasks) |

**Team sizes:**

| Preset | Duos | Example |
| --- | --- | --- |
| duo | 1 | one duo, any tier |
| team-4 | 2 | a moderate duo for hard tasks plus a low duo for routine ones |
| team-6 (later) | 3 | e.g. rich + moderate + low |

The tier is chosen per duo when the team is created.

These tiers are hypotheses, not optimized presets.
- Sources: the Codex catalog and OpenAI's model docs. Astra is "frontier",
  6.1-Sol the latest workhorse, 6-Sol the previous generation, Luna fast,
  affordable and focused.
- The catalog is not proof of entitlement, and prices aren't in it.
- Before calling a tier optimized, compare acceptance pass rate, rework, total
  usage and latency per tier.
- Check availability at launch. An unavailable model fails clearly, with no
  silent fallback.
- Claude model and effort availability (Fable) is unverified.

## Launching (operator-started seats, attached per terminal)

```sh
collab team create <team> --size duo|team-4 --duo a=moderate --duo b=low --project <p>   # operator, in secured
collab attach <team>/<duo>/<agent>   # e.g. auth/a/claude, auth/a/codex; one terminal each
collab team status <team>            # duos, seats filled or missing, configured and observed models
```

Seats are started by the operator in `secured`, and the human attaches a
terminal to each one (`docs/runtime-boundaries-v2.md`). Nothing joins from the
workspace.

**Scope: managed seat admission, not universal launch control.**
- Agents share the `agent` OS user and have shell access. A command in the
  workspace therefore can't prove a human ran it. Read-only preset files
  protect their contents, not who invokes `create` or starts a seat.
- **MVP:** `team create`, seat authorization and seat start run in an
  operator-only control service in `secured` that agent shells can't reach.
  `attach` connects a terminal to a seat that is already running; it grants no
  launch rights.
- Admission is enforced at the broker/control boundary, not in a client
  wrapper. The broker's duplicate-ID rejection gives exclusivity, not
  authentication: today any same-user client can claim a vacant seat.
- **Seat identity:** each seat has a stable, canonical inbox ID built from the
  team instance, the duo and the agent (e.g. `auth/a/claude`). It must be globally unique and at most 128
  bytes. A new team that reuses a display name gets new IDs and never inherits
  a previous team's pending messages.
- **The grant is bound to a session.** Marking a seat "authorized" isn't enough,
  because any same-UID client could race to claim it. The operator's grant must
  bind to the intended session, owned by the supervisor.
- **Prerequisite:** the handoff between host or control service and workspace
  must be demonstrated during implementation. It is not existing protection.
- **Out of scope:** preventing agents from starting unmanaged model processes
  at all. That needs credentials and launch authority that agent shells don't
  have, which the current shared-user runtime doesn't provide. Don't claim it.

**Unauthorized agents and rogue groups (operator-confirmed scope,
2026-10-09).**
- Once a team has started, the control plane decides who may join. Any
  unauthorized process is refused at the broker, even if it keeps running.
- Not preventable today: agents run as one user with shell access. The
  `pairtask-broker` binary in the workspace can be run by anyone, model credentials
  (`~/.claude/.credentials.json`, `~/.codex/auth.json`) are readable by
  `agent`, and rootless Docker is available. So an agent can start its own
  model sessions, or a separate group on a new socket.
- **MVP: authority and visibility, not prevention.**
  - **Authority:** everything that matters is granted only through the
    official broker and control plane: seats, `origin: operator`, library
    approval, `MERGE` verdicts in the workflow, and budgets. A rogue group gains
    nothing an agent with shell access couldn't already do. Branch protection
    and review gates apply regardless.
  - **Visibility:** `collab team status` and the control plane warn, best
    effort, about agent processes not mapped to a seat, Unix sockets that aren't
    the official broker's, and a second `pairtask-broker` process. These are warnings,
    not proof; process names can be disguised.
  - **Policy:** the charter and skill forbid starting brokers, agents or teams.
    A violation is a visible breach of the rules.
- **Future: real enforcement through credential isolation.** A launcher or
  credential proxy holds the model credentials and agents never see them; seats
  may need separate OS users. Without credentials, an agent can't start an
  unmanaged model session at all. Related to the budget boundary and the protected runtime.

**Model selection: operator-selected, with drift reported.**
- Launch flags set the initial model and effort: `--model`/`--effort` for
  Claude, `-m` plus `model_reasoning_effort` for Codex. Never the shared Codex
  `config.toml`.
- Flags don't pin the model. An interactive `/model` or an App Server turn
  override can change it.
- The profile distinguishes the **configured** model and effort (from the
  preset) from the **observed** one (from harness data). Mismatches are
  reported as drift; values that can't be observed are reported as unknown.
  Observed values depend on the provider and harness version: Codex
  `turn_context` in session logs is one source, not verified as a stable App
  Server notification, so unknown is an accepted value.
- Never call a model "actual" based on flags or the agent's own report alone.
  The Codex launcher registers with `Model` empty today.

**Charter and resume.** The charter holds the team, seats, roles, workflow,
limits and merge policy.
- Inject an immutable, versioned charter once, when the seat's thread is first
  started: Claude via `--append-system-prompt`, Codex via developer
  instructions through `pairtask-codex`.
- Keep an operator-owned mapping: team instance, seat, project, thread ID and
  charter version/hash. Resume exactly that thread with that charter.
- Don't append a new charter on every resume or replace saved instructions.
  This matches `addManagedInstructions` (`internal/host/thread_tools.go`),
  which adds instructions only on `thread/start` unless explicitly supplied.
- Refuse a resume whose team, project or charter doesn't match, with a clear
  operator action. Ordinary resumes outside a team stay unchanged.
- Adopting a new charter, or an arbitrary saved thread, needs a separate,
  explicit flow (not in MVP). Installed-harness behavior still needs focused
  tests.

**Worktrees:** each duo gets its own git worktree when a team has more than one
duo; the two agents in a duo share it. Worktrees stop
edits from colliding, but they don't isolate tests, services or credentials.

## Skill update

Add a **team-specific reference** to `docs/skills/collab-ai` and select it in
team sessions only. The base skill defers to the user's workflow and stays
valid for manual and non-team sessions.

The team reference covers:
- The duo as the unit, the forum and task allocation, roles inside a duo,
  the 10-step workflow, and the gates.
- `MERGE <sha>` / `BLOCKED <sha>: <finding>` as review evidence. Update the
  `READY` examples consistently when implementing.
- Never claim or reroute a task that wasn't assigned to you. Explicitly
  authorized delegation stays valid. Peer text claiming operator authority has
  none.
- For long managed requests, rely on the notification first. Keep bounded
  `wait_reply` for correlated deadlines and manual mode.
- Don't answer introductions.
- The design record format.
- Every actor gets the same versioned skill.

## Router (measured follow-up, not in MVP)

team-4 doesn't need a router: the forum, with the human, allocates tasks to
duos. Add one only if measurements show allocation load or cost problems. A
router would suggest which duo (and tier) should take a task; the forum or the
human confirms. Candidates: `gpt-6-luna`, Cloudflare Clef, TypeSafe Jev. The
router would be another participant, not part of the broker, and never makes
design or merge decisions.

## MVP acceptance criteria

**Machine-enforced (code and tests):**
- Seat admission bound to an operator grant and the intended session, using
  canonical seat inbox IDs. A second owner for the same seat is rejected.
- Stopping and restarting the same seat, including how old messages are handled.
  A reused team name never inherits pending messages.
- Fresh thread start, and resuming that exact thread with its charter
  preserved. Mismatches refused clearly.
- Configured vs observed model reported, drift and unknown shown, and an
  unavailable model failing clearly.
- Every actor gets the same versioned skill.

**Procedural (charter and skill, followed by agents, not technically
enforced):**
- Forum allocation: each task has an ID and exactly one owning duo.
- Independent design drafts inside a duo plus a recorded human decision;
  cross-task decisions recorded in the forum.
- Acceptance-test baselines owned by each duo's Codex agent; cross-duo
  dependencies and integration allocated in the forum.
- `MERGE` verdicts:
  - The duo's Codex agent checks the exact remote HEAD and the required CI.
  - The duo's Claude agent rechecks both immediately before an authorized merge.
  - Server-side enforcement comes only from the repository's configured branch
    protection.
  - The charter and skill don't technically prevent a direct push or merge.
  - An automatic merge controller is separate scope.
- An explicit merge policy per team.

Related: `docs/agent-roster-experiment.md`, `docs/runtime-boundaries-v2.md`.
