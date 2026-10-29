# Live host handoff (2026-09-15)

Validated active and idle push delivery with real model tool calls over an
isolated UDS broker (`84fdead`) between **Claude Code 2.1.272** (interactive,
development channel enabled) and **Codex CLI 0.154.0** (App Server thread via
`collab-codex`), with neither agent polling `receive` or `wait`.

| Host | Conversation ID | Broker identity / session |
| --- | --- | --- |
| Claude Code | `b8eca74e-8e36-405e-84b6-aa424c3788d1` | `claude-live` / `1b6edee3-d291-408c-9a72-c8bb6e123baf` |
| Codex App Server | `01a0a55a-1bc4-7270-81e9-887fb6c9107e` | `codex-live` / `f76bbdd4-5c18-4cfa-8d9a-5c1af6e669f7` |

| Message ID | Recipient state at arrival | Adapter receipt (UTC) | Persisted `agent_acknowledged` (UTC) |
| --- | --- | --- | --- |
| `demo-claude-idle` | Idle interactive session | 13:55:59.995506 | 13:56:02.419389 |
| `demo-codex-idle` | Idle App Server thread (woke turn `01a0a55a-8c79-7290-bf15-0baf876ea6f3`) | 13:56:09.977398 | 13:56:14.042283 |
| `demo-claude-active` | Running `sleep 15` (`13:56:24.185`–`13:56:40.299`, `toolu_01VZLvC7DMLLMWUQfTxXJGhY`) | 13:56:30.045103 | 13:56:41.875970 |
| `demo-codex-active` | Holding `demo_checkpoint` (`13:56:50.052`–`13:56:54.790`, turn `01a0a55b-1cc9-7b31-ba57-705c4caabc2d`, ack `exec-cd3e0295-672f-432d-8da0-dcfd447e7b49`) | 13:56:52.634763 | 13:57:00.730537 |

## Limits observed

- Non-interactive `claude -p --input-format stream-json` connected MCP and
  recorded `adapter_received`, but did not load the development channel or emit
  `agent_acknowledged`. Interactive startup with channel consent and the
  "Channel notifications registered" diagnostic is required.
- Proves the four delivery cases for these versions and settings; does not
  prove process-exit recovery, org-policy availability, or attachment to an
  already-running Codex conversation. See [host integration](host-integration.md).
