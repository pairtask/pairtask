# Team presets and collaboration workflow (draft)

**Status: draft (2026-10-12).** Not implemented. Builds on
[agent roster experiment](agent-roster-experiment.md) and
[runtime boundaries](runtime-boundaries-v2.md).

## Principles

1. **The duo is the working unit.** Every task is owned by one duo pairing a
   Claude agent and a Codex agent. No design, implementation, or review rests
   on a single model.
2. **Teams are multiples of duos plus a forum.** The forum (`pairtask chat` over
   the operator control plane) connects the human and the duos to split work,
   record cross-task architecture decisions, and allocate each task to one duo.
3. **The human makes final decisions.** Duos converge and record agreements and
   open disagreements; the human decides.
4. **Roles follow operator experience:** Claude leads open-ended design and
   implementation; Codex writes acceptance tests and leads strict review.
5. **Cost is tuned per duo tier**, not by splitting solo specialists.

## Duo workflow

| # | Step | Actor |
| --- | --- | --- |
| 1 | Discuss allocated task and scope | Claude |
| 2 | Independent drafts → critique → single design record (agreements, disagreements, human decision needed) | Claude ↔ Codex |
| 3 | Write acceptance tests | Codex |
| 4 | Implement (starts only after human approves step 2's design record) | Claude |
| 5 | Review and add regression tests | Codex |
| 6 | Follow-ups and fixes | Claude |
| 7 | Open the PR | Claude |
| 8 | Run test suite | CI |
| 9 | Validate exact remote HEAD: `MERGE <sha>` or `BLOCKED <sha>: <finding>` | Codex |
| 10 | Merge (or hand to human per team policy) after green CI, required gates, and `MERGE <sha>` | Claude / human |

- Any new commit invalidates an earlier `MERGE <sha>` verdict.
- `MERGE <sha>` is review evidence, never merge permission on its own.
- Duos branch from reviewed baselines and never weaken another duo's acceptance
  tests; cross-duo changes go through the forum. Multiple duos use separate git
  worktrees (one worktree per duo).

## Presets and model tiers

| Tier | Claude agent | Codex agent |
| --- | --- | --- |
| `rich` | Fable | `gpt-6-astra` (high) |
| `moderate` | Opus (medium) | `gpt-6-astra` (medium) or `gpt-6.1-sol` (high) |
| `low` | Opus (medium/low) or Sonnet | `gpt-6.1-sol` (medium) or `gpt-6-luna` |

- **Sizes:** `duo` (1 duo, any tier), `team-4` (2 duos, e.g. `moderate` +
  `low`), `team-6` (later, 3 duos).
- Tiers are hypotheses to be validated against pass rate, rework, token usage,
  and latency. Unavailable models fail clearly at launch without silent
  fallback.

## Seat admission, authority, and charter

```sh
collab team create <team> --size duo|team-4 --duo a=moderate --duo b=low --project <p>   # in secured
collab attach <team>/<duo>/<agent>   # e.g. auth/a/claude, auth/a/codex; one terminal each
collab team status <team>            # duos, seats, configured vs observed models
```

- **Managed seat admission:** `team create`, seat grants, and seat start run in
  `secured` outside agent shell reach. `attach` connects a terminal to a
  running seat without granting launch rights.
- **Canonical seat IDs:** each seat has a globally unique inbox ID (up to 128
  bytes, e.g. `auth/a/claude`) bound to a supervisor-owned session grant so
  same-UID processes cannot race to claim it. Reusing a team display name
  creates fresh seat IDs and never inherits old pending messages.
- **Shared-user workspace limits:** because agents share the `agent` OS user in
  `workspace` with readable credentials and binaries, the MVP enforces
  **authority and visibility** rather than preventing unmanaged processes:
  official authority (`origin: operator`, seats, budgets, review verdicts)
  flows only through the control plane, while `team status` warns on unmapped
  agent/broker processes or sockets. Full prevention requires credential
  isolation in `secured` (see [runtime boundaries](runtime-boundaries-v2.md)).
- **Configured vs observed model:** launch flags (`--model`/`--effort` for
  Claude, `-m`/`model_reasoning_effort` for Codex) set the initial model
  without mutating shared `~/.codex/config.toml`. `team status` reports
  configured vs observed model/effort (or `unknown`), surfacing interactive
  `/model` drift.
- **Immutable charter and resume:** an operator-owned mapping binds team
  instance, seat, project, thread ID, and charter hash. The versioned charter
  is injected once on `thread/start` (`--append-system-prompt` for Claude,
  developer instructions for Codex) and preserved on resume; mismatched team,
  project, or charter resumes are refused.
- **No router in MVP:** the human and forum allocate tasks for `duo` and
  `team-4`.

Related: [agent roster experiment](agent-roster-experiment.md),
[runtime boundaries](runtime-boundaries-v2.md).
