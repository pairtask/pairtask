# Runtime boundaries v2 (draft)

**Status: draft (2026-10-09).** Partially implemented: the offline control
broker and operator control plane in `secured` are shipped ([control
runtime](secured-runtime.md)); operator-started seats, protected `attach`,
credential separation, and policy-mediated egress remain design targets for
[team presets](team-presets.md).

## Threat and authority boundary

- **Trusted:** the host operator, Incus administrators, root in `secured`, and
  supervisor-managed model client processes in `secured`.
- **Untrusted:** all processes running as `agent` in `workspace` (where agents
  share one UID, readable model/GitHub credentials, and outbound NAT on
  `incusbr0`), plus all peer messages.
- **Goal:** seats, `origin: operator`, approvals, budgets, and model
  credentials are obtainable only through operator-controlled paths in
  `secured`, with residual channels stated explicitly.

## 1. Control plane in `secured` and protected `attach`

- **Shipped:** the broker, SQLite store, and `SO_PEERCRED`-checked operator
  socket run in `secured`; `workspace` mounts only the client socket and has
  read-only `status`/`dashboard` views.
- **Planned seat control and `attach`:**
  - `team create`, seat start/stop, and budget administration run only in
    `secured`. No agent joins from `workspace`.
  - `attach` connects from the host or `secured` to an operator-authenticated
    session proxy owned by the supervisor (never the raw App Server, broker, or
    executor socket).
  - Config, credential-file, and local command-execution requests remain
    unreachable from the attached UI; resume is bound to the server-owned
    thread, charter, and budget; approval responses are validated against
    pending requests; accounting survives UI detach.

## 2. Splitting model clients from workspace tool execution

**Current offline proof** ([budget boundary](budget-boundary.md),
`pairtask-supervised-codex`, `infra/image/secured-codex.toml`):

| Component | Runs in | Current state |
| --- | --- | --- |
| Model client | `secured` (systemd unit, root-only home) | Offline fixture provider; hooks/plugins/multi-agent disabled; runs as root (no per-seat isolation yet) |
| Tool execution | `workspace` as `agent` | Per-connection `exec-server` over root-owned mode-`0600` `/mnt/collab-executor/codex.sock` (`include_local = false`), or Claude `--restricted --tools "" --strict-mcp-config` |

- **Remaining before production cutover:**
  - Validate real subscription auth, OAuth refresh, streaming, interactive
    terminals, and resume with the model client in `secured`.
  - Validate interactive Claude building with restricted built-ins and
    workspace-scoped MCP tools (read, search, edit, shell), confirming hooks,
    plugins, and slash commands cannot regain local execution in `secured`.
  - A workspace-reachable credential gateway or `apiKeyHelper` does not hide
    credentials from same-UID workspace processes; the model client itself must
    stay in `secured`.
  - Remove model credentials from `workspace` in a single cutover once
    authorized paths work. Keep any residual repository-scoped GitHub token in
    `workspace` explicit, or move privileged GitHub writes (PR creation, merge)
    into `secured`.

## 3. Egress: opaque CONNECT proxy and seat attribution

- **Design:** model clients hold credentials inside `secured` (which has no
  NIC) and reach providers through a trusted loopback relay → Unix socket →
  external opaque `CONNECT` proxy. TLS stays end-to-end; the proxy enforces
  destination allowlists and logs seat binding, destination, bytes, and timing.
- **Seat attribution and revocation:** each seat uses a dedicated socket never
  mounted into `workspace`. Revoking a seat closes active relay/proxy streams
  and stops the supervised client.
- **Acknowledged residual channels:**
  - Permitted general HTTPS endpoints (e.g. GitHub, package registries) cannot
    prevent data exfiltration by allowlist alone.
  - DNS tunneling requires resolving approved destinations only at the proxy and
    blocking direct DNS, DoH, DoT, and UDP/QUIC bypasses across the bridge and
    rootless Docker.

Related: [team presets](team-presets.md), [budget boundary](budget-boundary.md),
[control runtime](secured-runtime.md).
