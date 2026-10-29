# Troubleshooting

[Back to the README](../README.md).

| Symptom | What to check |
|---------|---------------|
| Missing socket / connection refused | Start the broker first and match every `--socket` to `PAIRTASK_SOCKET_PATH`. For a socket in your current directory, use `"$PWD/collab-ai.sock"` (uppercase `PWD`, no extra leading slash). |
| `duplicate_id` | Another session owns that agent ID. Close its adapter or choose a distinct ID; a second connection cannot take over the inbox. |
| Claude tools work but no automatic messages | Check channel opt-in, authentication, and organization policy. Use the [interactive channel launch](host-integration.md#claude-code). |
| Codex resume rejects permission flags | Resume with saved permissions; remove `--sandbox` / `--ask-for-approval` overrides. |
| Delivery stops after a disconnect | Restart the affected adapter or launcher with the same agent ID. Accepted, unacknowledged durable messages replay; use `resume` to also restore Codex conversation history. |
| `cs-mcp auth` (or the CodeScene MCP `login` tool) appears to hang inside the workspace | It uses CodeScene's browser OAuth flow; the default headless workspace provides no browser and no callback path for it. Cancel the waiting command with Ctrl+C. Create a [personal access token](https://codescene.io/users/me/pat) and run `pairtask-codescene-login` in your sandbox terminal; it saves the token privately under `~/.config/codehealth-mcp/`, then restart the agents. |
| `CERTIFICATE_VERIFY_FAILED` from a host script | That Python trusts no certificate authority. On macOS with a python.org build, run `/Applications/Python 3.X/Install Certificates.command` once; otherwise use `/usr/bin/python3`, or install `certifi`, which the host scripts pick up automatically. |

The broker refuses an existing socket path. After a crash, verify the broker is
no longer running before removing a stale socket. Normal shutdown cleans up its
own socket. The broker's environment variables are optional:
`PAIRTASK_SOCKET_PATH` defaults to `/tmp/collab-ai.sock`, and `PAIRTASK_DB_PATH` defaults
to `./collab-ai.db`.

Older `COLLAB_*` names (`COLLAB_SOCKET_PATH`, `COLLAB_DB_PATH`, `COLLAB_BUDGET_DIR`,
`COLLAB_OPERATOR_HOME`, …) still work: they apply only when the `PAIRTASK_*`
name is unset, and print one deprecation warning on stderr. A `PAIRTASK_*` variable
set to an empty value is an error for settings with a default; unset it to use the
default.

For MCP registration errors, see [adapter troubleshooting](mcp.md#troubleshooting).
For host-specific behavior, see [Codex and Claude setup](host-integration.md).
