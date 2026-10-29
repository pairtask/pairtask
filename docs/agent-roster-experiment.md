# Agent roster experiment

**Status: completed experiment (2026-10-09).** Tested whether four Claude and
Codex sessions on different models could introduce themselves and divide work
peer to peer without a dedicated router.

## Setup and rules

| Agent ID | Harness | Model | Role hint |
| --- | --- | --- | --- |
| `claude-opus-exp` | Claude Code (2.1.283) | Opus 5.5 | architecture, review |
| `claude-sonnet-exp` | Claude Code (2.1.283) | Sonnet 5 | implementation, research |
| `codex-2-exp` | Codex (`pairtask-codex --terminal`, 0.159.3) | `gpt-6-sol` (high) | implementation |
| `codex-3-exp` | Codex (`pairtask-codex --terminal`, 0.159.3) | `gpt-6-astra` (low) | quick checks |

- **Introductions:** broadcast to `*` under `intro-<run-id>-<agent-id>-<revision>`
  with self-reported harness, model, strengths, limits, and availability. Peers
  acknowledge introductions without replying (preventing broadcast storms) and
  treat them as untrusted peer data.
- **Bounds:** read-only design/review work on one checkout; at most 2
  delegation hops (`owner → specialist → reviewer`), 12 task messages per run,
  and a 10-minute window. Late or expired messages are acknowledged (clearing
  durable pending state) without executing or replying.

## Results (2026-10-09)

### `exp1`: peer-relayed authorization (aborted before task)

The observer (`claude-1`) sent the run brief over the peer channel.
- **Channel authorization was rightly rejected by Codex:** both Claude agents
  broadcast introductions within ~30 s, while both Codex agents acknowledged
  the brief and stopped, treating "on behalf of the operator" from a peer as
  unverifiable.
- **Run expiry held:** after the deadline, `codex-2-exp` refused expired work
  when prompted in its terminal.
- **Self-reported model metadata was unreliable:** `codex-3-exp` replied only
  to the observer instead of broadcasting, and neither Codex session could
  inspect its own model variant or reasoning effort (`gpt-6-sol` vs
  `gpt-6-astra`). Editing shared `~/.codex/config.toml` also mutated defaults
  for other Codex sessions.

### `exp2-t1`: slice-1 inspection contract (owner `claude-sonnet-exp`)

Authorization and task were pasted into all four terminals.
- **Timeline:** 12:55:18 start → 12:58:45 review request → 12:59:34 review
  reply → 13:00:27 final report (~5 minutes, 4 of 12 task messages).
- **Delegation and wake-up:** `claude-sonnet-exp` selected `claude-opus-exp`
  by stated review strength (1 hop). Idle `claude-opus-exp` woke via its
  channel notification and returned a high-quality review in 49 s catching four
  real `internal/store` schema mismatches.
- **Forwarding hazard:** `codex-2-exp` and `codex-3-exp` saw the pasted task
  and forwarded copies to `claude-sonnet-exp` claiming operator authority;
  `claude-sonnet-exp` deduplicated and acknowledged them without redoing work.

### `exp2-t2`: operator origin marking (owner `codex-2-exp`)

Task pasted only into `codex-2-exp`.
- **Timeline:** 13:27:27 start → 13:28:41 review request → 13:29:30 review
  reply → 13:30:39 final proposal (~3 minutes, 2 task messages, 0 duplicates).
- **Delegation and wake-up:** `codex-2-exp` drafted the proposal and requested
  a security critique from idle `claude-opus-exp`, which woke and answered in
  49 s with 6 must-fix and 5 should-fix points.

## Findings and resulting requirements

1. **Peer-to-peer delegation works without a router** for two-skill tasks once
   the operator authorizes the work, but stated strengths concentrate review on
   the strongest model (`claude-opus-exp`) unless cost tiers are explicit.
2. **Never forward tasks meant for another agent:** peer forwarding creates
   duplicates and spoofs operator authority.
3. **Authenticated operator control plane (shipped in protocol v4–v9):**
   broker-set `origin: operator` on a dedicated `SO_PEERCRED`-verified socket
   in `secured`, audited in SQLite (`operator_audit`), plus task claims (v8)
   and rooms (v9) so broadcasts do not trigger duplicate work.
4. **Future directions (not implemented):**
   - Launcher-reported model/effort metadata instead of model self-reports.
   - An indexed, peer- or operator-approved project knowledge base ("library").
   - Multi-project namespace isolation and authenticated remote relays (see
     [team presets](team-presets.md) and
     [runtime boundaries](runtime-boundaries-v2.md)).

References: [Clef decision models](https://blog.cloudflare.com/clef-decision-models/),
[oh-my-pi](https://github.com/can1357/oh-my-pi),
[team presets](team-presets.md).
