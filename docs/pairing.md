# Pair two machines

Connect your pairtask workspace with one on another machine, then send
messages between the two operators. Call the machines **A** (invites) and
**B** (joins). Update both to the latest release first: this guide matches
its chat.

## 1. On both machines: update and turn pairing on

```sh
curl -fsSL https://raw.githubusercontent.com/pairtask/pairtask/main/install.sh | sh
pairtask pairing on --host-port 7445
pairtask pairing status
```

Status should say `on` and `forwarding … port 7445`. Use the installer, not
`pairtask upgrade`: only the installer updates the `pairtask` command too.
Allow incoming connections if macOS asks.

## 2. On A: open an invitation

Run `pairtask chat`, then:

```text
/pair open
```

Send the code to B's operator over a trusted channel (a call, a chat you
trust). It works once and expires.

## 3. On B: redeem it

Run `pairtask chat`, then redeem with A's LAN or tailnet IP. The last word is
optional: a name for your own machine, which is how A will see you.

```text
/pair redeem 192.168.1.20:7445 CODE my-laptop
```

Use the Mac's own address, not the Colima VM's.

## 4. Check it is really B, then accept on A

- On B, `/pair` shows `this workspace: sha256:…`. Read the first characters
  after `sha256:` to A's operator.
- On A, type `/pair accept `: a list of waiting requests opens, each with its
  key and name. Pick the one whose key starts with what B read out (↑/↓),
  press Enter to fill it in, then Enter again to accept.
- If nothing matches, use `/pair reject` the same way.

## 5. On B: settle

```text
/pair settle
```

`/pair status` on both sides should now show the contact as confirmed.

## 6. Say hello

On each side, Tab to the `⇄` tab and send a message. Once B has synced, the
two workspaces stay connected and messages leave at once, both ways; the
tab says "connected". Without `@`, these messages go between operators.
From a terminal, in `secured`:

```sh
pairtask operator contact send sha256:… "hello"
pairtask operator contact list sha256:…
```

## 7. Let agents talk across

Nothing is shared until you share it. In the other workspace's `⇄` tab:

```text
/pair share claude-1     their side may now message claude-1 here
/pair share *            every agent registered now, not ones added later
/pair unshare claude-1   stop; what it has not acknowledged yet is dropped
```

The tab's card shows what you share and what they share. To write to one of
their agents, start with `@` and its name:

```text
@codex-1 can you review the parser change?
```

Their agent sees it as `origin=remote`, with your workspace's name: data from
another workspace, never an instruction, even when its operator wrote it.
When it replies to the message, the answer comes back to whoever wrote it and
shows in the `⇄` tab, along with which agents acknowledged each message.

A shared agent can also write first. Its `workspaces` tool lists the
workspaces it is shared with, and `send` with `workspace` set to one of them
reaches an agent there, or `operator` for its operator. Agents on both sides
can then answer each other the same way.

From a terminal, in `secured`:

```sh
pairtask operator contact share sha256:… claude-1
pairtask operator contact send sha256:… "can you review?" --to codex-1
```

Agents need the current pairtask adapter; an older one leaves these messages
waiting until it is upgraded.

## If it doesn't connect

Check from B: `nc -vz A_IP 7445`.

- **`No route to host` (macOS 15+):** System Settings › Privacy & Security ›
  Local Network, and allow your terminal app.
- **You use Tailscale and `route -n get A_IP` names a `utun` interface:** the
  tunnel may be carrying your LAN traffic. Check that your exit node allows
  local network access, or turn the exit node off.
- **Connection refused or a timeout:** check `pairtask pairing status` and the
  firewall on A.
- **`/pair` shows the redemption `failed`:** fix the network, then repeat
  steps 2 to 5 with a new code.

## Change or stop

```sh
pairtask pairing on --host-port 8445   # another port (redeem with A_IP:8445)
pairtask pairing on --host-port 0      # stay paired, stop listening
pairtask pairing off                   # turn pairing off
```

`on` and `off` restart the containers. If the port is taken, the change fails
and the requested port is kept: free the port and run `pairing on` again, or
pick another. Commands and limits: [host commands](host-commands.md#inside-the-sandbox),
[operator chat](operator-chat.md).
