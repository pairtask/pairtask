# Automatic terminal communication (2026-09-23)

Validated **Codex CLI 0.156.1** (`collab-codex --terminal`, thread
`01a0ce6f-6612-72a0-adb7-c648e3a46920`, inbox `codex-managed-test`, session
`d7445da1-c963-4165-a919-55481200824b`) receiving, explicitly acknowledging,
and replying to fixture messages without calling `listen`, `receive`, or `wait`
across idle, `/compact`, and active shell execution.

**Claude Code 2.1.280** (`claude-managed-test`, session
`ff83db79-b094-4900-96c9-92468812ddfa`) registered with `--claude-channel
--auto-listen` and persisted `adapter_received`, but the host reported
"Channels are not enabled for your org" and did not expose the message to the
model.

| Case / message ID | Adapter receipt (UTC) | Explicit acknowledgment (UTC) | Correlated reply ID |
| --- | --- | --- | --- |
| Idle: `auto-cli-idle` | 13:23:37.431719 | 13:23:44.591569 | `0c4feec1-41a5-453d-a3bc-0ae2c56b0f27` |
| After `/compact` at `13:25:28.660`: `auto-cli-after-compact` | 13:26:01.911496 | 13:26:06.879145 | `c4a7c17f-83b7-472d-9b6d-c62b4005b2d6` |
| Active `sleep 30` (`13:28:36.480`–`13:29:06.555`): `auto-cli-active` | 13:28:45.569069 | 13:28:54.213265 | `0ece6375-8165-4bdd-9c2a-1002e0057556` |
| Claude org-policy blocked: `auto-claude-idle` | 13:25:31.681181 | None observed | None observed |

## Findings and limits

- **Host-frame scanner limit:** Codex 0.156.1 `plugin/list` returned
  10,451,619 bytes on startup, exceeding the old 8 MiB proxy scanner limit. The
  App Server host-frame limit is now 32 MiB (broker peer frames remain capped
  at 1 MiB).
- **Limits:** does not establish automatic reconnect after process exit,
  attachment to an already-running Codex conversation, or two-host live
  acceptance when org policy blocks Claude channels. See
  [host integration](host-integration.md).
