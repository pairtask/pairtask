# Runtime boundaries v2 (draft)

**Status: draft, reviewed by Codex 2026-10-09.** Not implemented. Foundations
under `docs/team-presets.md`, which assumed a workspace-side `collab join`;
this replaces it with operator-started seats and a protected `attach`.

## Problems with today's runtime

- **Control commands live where agents are.** The `pairtask` binary is installed
  in the workspace. Agents run as the same user (`agent`), so "only the human
  runs this" can't be enforced there.
- **Agents can read credentials.** `~/.claude/.credentials.json`,
  `~/.codex/auth.json` and the GitHub token (`~/.config/gh/hosts.yml`) are
  readable by every agent. So is the `pairtask-broker` binary. Any agent can start
  unmanaged model sessions or a separate group.
- **Outbound traffic is unfiltered.** The workspace has an ordinary connection
  on `incusbr0` with outbound NAT and no filtering (`infra/incus/main.tf`).
  `secured` has no network card (`secured.tf`).

## Threat and authority boundary (define first)

- **Trusted:** the host operator, Incus administrators, root in `secured`, and
  the model client processes the supervisor runs there.
- **Untrusted:** everything running as `agent` in the workspace (agents, their
  tools, any process they start), and every message from peers.
- **Goal:** authority (seats, operator origin, approvals, budgets, model
  credentials) is only obtainable through operator-controlled paths in
  `secured`. Residual channels are named explicitly, not hidden.
- **Not a goal yet:**
  - validating intent; a malicious tool can still steer its legitimate model
    client
  - per-seat isolation between model clients in `secured`, which share root
    today and could impersonate each other

## 1. Control plane in `secured`, and protected `attach`

- Control commands exist **only in `secured`**: team create, seat start and
  stop, operator send and fan-out, extend and stop runs, and later library
  approval. Reached from the host as the operator (`incus exec` or SSH into
  `secured`). The workspace keeps a read-only client (status, dashboard).
- **Seats are started by the operator in `secured`.** Nobody joins from the
  workspace.
- **`attach` is new integration work, not existing wiring.**
  - Restricted Codex rejects terminal mode, MCP relay and forwarded arguments
    (`cmd/codex/restricted.go`). Its operator policy rejects resume, fork,
    config and filesystem requests, and all server responses
    (`internal/host/operator_policy.go`). Approval or input requests fail the
    budget.
  - The terminal wrapper owns the App Server's lifetime and closes it on exit,
    so there is no detach and re-attach today.
- **Design for `attach`:**
  - It reaches an **operator-authenticated session proxy owned by the
    supervisor** (or a controlled PTY for Claude). Never the raw App Server,
    broker or executor socket.
  - Config, credential-file and command-execution requests stay unreachable
    from the UI.
  - Resume is bound to the server-owned thread, charter and budget.
  - Approval responses are validated against pending requests.
  - Supervisor accounting continues across a UI detach.
  - Don't disable the restricted ingress just to make the standard terminal UI
    work.
- **Attach from the host or control side first.** A writable attach endpoint
  that agents (same UID) in the workspace can reach can't prove that input or
  approvals came from a human.
- The broker sets the operator origin from the connection, per the exp2-t2
  proposal: an operator socket in `secured`, checked with `SO_PEERCRED`.

## 2. Credentials: split model client and tool execution

**Today: an experimental offline proof only** (`docs/budget-boundary.md`,
`pairtask-supervised-codex`, `infra/image/secured-codex.toml`):

| Part | Runs in | Today |
| --- | --- | --- |
| Model client | `secured`, systemd unit, root-only home | offline fixture provider, no real credentials; hooks, plugins, apps and multi-agent disabled; runs as **root**, so no per-seat isolation |
| Tool execution | workspace, as `agent` | a workspace exec-server per connection; it shares `/home/agent` with ordinary sessions, so the workspace still has credentials today |
| Link | root-owned, mode-0600 `/mnt/collab-executor/codex.sock` | `include_local = false`; the proof checks selected denials of local tools, not every file, plugin or config path |

- **Codex:** execution environments with local execution disabled.
- **Claude:** `--restricted --tools "" --strict-mcp-config` plus approved MCP
  tools in the workspace. Per the CLI docs, `--tools` limits built-in tools but
  leaves MCP tools, and `--restricted` removes command and code tools and
  restricts config.
- **Proven:** fake providers only, with `codex exec`, the App Server and
  `claude -p`.
- **Not proven:** interactive terminals, resume, subscription logins, real
  provider access.

**Plan:**
- **Codex:** the split with a real credentialed client in `secured`. Validate
  auth, token refresh and streaming with one real Codex login.
- **Claude: test the restricted split first.** It's a realistic candidate, not
  yet proven for interactive building. Required:
  - MCP tools for read, search, edit and shell on workspace paths
  - cancellation, permissions and output limits
  - resume, and collab notifications with channel consent
  - proof that hooks, plugins and slash commands can't regain local execution
- **A credential gateway doesn't replace process separation.** Any workspace
  process can use a gateway credential or socket that the workspace can reach.
  `apiKeyHelper` hands the credential to the client, so it isn't hidden from the
  same user. Per Claude's gateway docs, gateway credentials replace
  subscription auth and bill the forwarded credential. `BASE_URL` alone keeps
  the saved subscription OAuth, but in the client. Server-side OAuth injection
  and refresh is unverified.
- **GitHub: proposed residual, pending the operator's decision.** Keep a
  fine-grained token limited to the repository in the workspace, with minimal
  permissions, plus existing branch protection.
  - A repository scope narrows which repos the token reaches. It doesn't show
    which agent made an allowed write, and it doesn't preserve the human's
    merge authority.
  - Stronger alternative: move privileged GitHub writes (PR creation, merge)
    behind a trusted action in `secured`.
- **The cutover removes model credentials from the workspace** in one
  coordinated step, after the authorized paths work. The GitHub exception, if
  accepted, stays explicit.

## 3. Egress: opaque CONNECT proxy and placement attribution

**MVP decision:** model clients keep their own credentials inside `secured` and
reach providers through an **opaque CONNECT proxy**.
- The proxy enforces destination policy and logs seat binding, destination,
  bytes and timing.
- It can't see the model inside encrypted requests, and it can't inject
  credentials.
- Credential-injecting application gateways see plaintext, so they're a
  separate, deferred component.
- Log the configured model as configured. Take the observed model from trusted
  harness telemetry where available.

**Placement:** a separate egress container, or host-level, not `secured`.
- `secured` has **no network card; its egress is policy-mediated** (not
  "offline"): trusted loopback CONNECT relay → Unix socket → egress proxy, with
  TLS still end-to-end between client and provider. Claude documents
  `HTTP(S)_PROXY` (not SOCKS).
- Unverified for both clients: OAuth refresh, streaming, websockets,
  `NO_PROXY` behavior and socket permissions. These need tests per version.

**What attribution means:** traffic used an authorized seat channel. It
doesn't mean the model or client isn't rogue.
- Model API traffic is allowed only through the protected per-seat channels
  from `secured`.
  - Keep the tool-gateway listener separate from the per-seat model sockets.
  - Never mount model sockets into the workspace.
  - No shared listener where the caller picks the seat.
  - Validate mapped UIDs and modes.
  - **Revoking a seat** must close relay and proxy streams that are already
    open, and stop the supervised client where applicable. Removing a Unix
    socket or its permissions only blocks new connections, not existing ones.
    Acceptance evidence must show this.
- Workspace attempts to reach model APIs are **policy-violation signals, not
  proof** of a rogue agent; misconfiguration produces them too.
- Workspace tool traffic is attributable only to "workspace".

**Residual channels (acknowledged):**
- A domain allowlist can't prove there's no model use or exfiltration through
  permitted services such as GitHub or registries. Allowed general HTTPS
  remains a residual channel.
- DNS to a generic resolver can tunnel data. Constrain resolution: resolve
  approved destinations only at the proxy, and block direct DNS, DoH, DoT and
  alternate routes.
- Bridge-wide Incus ACLs don't cover traffic within the bridge unless applied
  to NIC devices, and baseline network services have exceptions. The network
  topology needs specific tests covering:
  - IPv4 and IPv6, UDP and QUIC
  - direct IP connections and redirects
  - how the proxy resolves destinations
  - paths over the same bridge
  - rootless Docker

## Order

1. Threat and authority boundary (above), agreed.
2. Fake-provider proof: a supervised session owned by `secured`, protected
   attach and resume, and tool isolation.
3. The protected model egress path, plus one real Codex validation of auth,
   refresh and streaming.
4. Claude interactive restricted-tools feasibility.
5. Coordinated cutover: remove credentials from the workspace and switch the
   workspace to deny-by-default. **Don't enable the workspace model deny rule
   before real model clients have a working authorized path.**
6. Then the team-presets MVP.

## Open questions

- What `attach` connects to for Claude (controlled PTY?) and how its
  interactive approvals are validated.
- Per-seat principals in `secured` (a separate user per seat's model client),
  or explicitly trusting all supervisor-run clients.
- Where the egress proxy runs on macOS (Colima VM vs a third container), and
  who administers it.
- One executor identity per seat vs a shared `collab-dev-executor`.

Related: [team presets](team-presets.md), [budget boundary](budget-boundary.md),
[control runtime](secured-runtime.md).
