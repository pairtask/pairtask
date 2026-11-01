# Pair two machines

Connect your pairtask workspace with one on another machine, then send
messages between the two operators. Call the machines **A** (invites) and
**B** (joins). Update both to the latest release first: this guide matches
its chat.

## 1. On both machines: update and turn pairing on

```sh
curl -fsSL https://raw.githubusercontent.com/pairtask/pairtask/main/install.sh | sh
pairtask pairing on --host-port 7445 --name my-laptop
pairtask pairing status
```

Status should say `on` and `forwarding … port 7445`. `--name` is how
workspaces you pair with from now on will see this one; see [Names](#names). Use the installer, not
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

- On B, `/pair status` shows `this workspace: sha256:…`. Read the first characters
  after `sha256:` to A's operator.
- On A, type `/pair accept `: a list of waiting requests opens, each with its
  key and name. Pick the one whose key starts with what B read out (↑/↓) and
  press Enter to accept it.
- If nothing matches, use `/pair reject` the same way.

## 5. On B: settle

```text
/pair settle
```

`/pair status` on both sides now lists the other workspace first, under
**Paired workspaces**, with a colored dot and words for how it stands:

- `connected`: a link is open, and messages leave at once.
- `pending`: not confirmed on both sides yet.
- `idle · last synced …`: no link open right now, which is not a fault.
- `last attempt failed …`: when it failed and why. See
  [If it doesn't connect](#if-it-doesnt-connect).
- `not yet observed`: no sync attempt since this broker started.

Below that, **Waiting for you** lists what needs your decision, and
**Waiting for them** what the other side still has to do. If a refresh
fails, the last state stays on screen with the time it was read.

## 6. Say hello

On each side, Tab to the `⇄` tab and send a message. The tab is titled
with the other workspace's name; to call it something else, see
[Names](#names). Once B has synced, the
two workspaces stay connected and messages leave at once, both ways; the
tab says "connected". Without `@`, these messages go between operators.
Their text shows with the same Markdown styles as local messages, with
terminal controls escaped; a diagram stays code. The mouse wheel and
PgUp/PgDn scroll the tab, and at the top load older messages.
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
In the tab, their agents show as `AGENT@WORKSPACE`, such as
`codex-1@Office`, with the workspace named as you call it (`/pair name`), so
they never read as yours. You still write to them as `@codex-1`.

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

## 8. Ask for reviews across

Their agents can ask yours for a pull request review, and yours theirs. In
the other workspace's `⇄` tab, decide what they may ask of each agent:

```text
/pair reviews claude-1 ask              each request waits for you
/pair reviews claude-1 auto --publish   it goes to claude-1 at once; it may publish a comment
/pair reviews claude-1 off              no more requests; waiting ones are dropped
```

`--formal` with `--publish` also allows a formal approval or change request.
A request asks for at most what you allow. A request for an agent you haven't
allowed is refused, and the `⇄` tab shows it, so you know it was asked for.
Allowing the agent afterwards only lets them ask again: they have to send a
new request. A request waiting for you shows in
the `⇄` tab: `/pair approve ID` or `/pair decline ID`. `/pair status` also
counts them per workspace under **Waiting for you**, so you see them without
opening each tab. Undecided ones expire after 7 days.

To ask one of their agents, pin the change to the commits you want reviewed:

```text
/pair review codex-1 OWNER/REPO#12 BASE_SHA HEAD_SHA [what to focus on] [--publish] [--formal]
```

An agent asks with its `request_review` tool. The reviewer reviews exactly
those commits: if the pull request moved on, the review can't start.

Where the request stands comes back to you: waiting for their operator,
declined, accepted, the verdict with its summary, and the published link.
The `⇄` tab shows it on your request; an agent that asked gets messages
with the latest status or result (steps in quick succession come as one),
and its `reviews_asked` tool lists its requests.

## Names

A paired workspace has a key and up to two names:

- **Its key** (`sha256:…`) is who it is. Messages, sharing and reviews go
  by the key, and the key stays the same as long as that workspace keeps
  its identity, across upgrades included. `/pair status` shows it.
- **The name it gives itself** is sent when it pairs: on B, the last word
  of `/pair redeem` when given; otherwise the one set with
  `pairtask pairing on --name NAME` (at most 64 bytes; `--name ""` clears
  it, and without `--name` the saved one stays). A broker run by hand takes
  `PAIRTASK_PAIRING_NAME`. The
  default, `workspace`, tells nobody apart, so chat doesn't show it as a
  name. The name is taken when you pair: setting or changing it reaches
  the workspaces you pair with afterwards, not the ones already paired,
  whose operators can still call it what they like with `/pair name`.
- **What you call it** is yours to choose. Set it in its `⇄` tab:

```text
/pair name office-m5     call it office-m5 here
/pair name --clear       go back to the name it gives itself
```

The name you choose, or else the one it gives itself, is used in the
tab's title, in its agents' labels (`codex-1@office-m5`) and in
`/pair status`, which adds "they call themselves …" when the two names
differ. With neither name, the tab shows the address you dial, or else the
first 12 characters of the key, such as `400221d946ad`. Agent labels never
use the address, because it can move.

What you call a workspace stays on your side. The other workspace never
sees it, and it changes nothing about who is paired, what is shared or who
may ask for reviews. A name is at most 64 printable bytes.

From a terminal, in `secured`:

```sh
pairtask operator peer name sha256:… office-m5
pairtask operator peer name sha256:… --clear
```

There, a workspace can be given as its key, a unique start of it, or a
unique name: what you call it, or what it calls itself.

Agents see the name you choose too. Their `workspaces` tool lists it as
`local_name`, next to the name the workspace gives itself and its key. `send`
with `workspace` takes the full key or either name, as long as exactly one
workspace has it; a key prefix doesn't count. A message an agent already
sent stays with the workspace it went to, even if you later give that name
to another one.

## If it doesn't connect

Check from B: `nc -vz A_IP 7445`.

- **`No route to host` (macOS 15+):** System Settings › Privacy & Security ›
  Local Network, and allow your terminal app.
- **You use Tailscale and `route -n get A_IP` names a `utun` interface:** the
  tunnel may be carrying your LAN traffic. Check that your exit node allows
  local network access, or turn the exit node off.
- **Connection refused or a timeout:** check `pairtask pairing status` and the
  firewall on A.
- **`/pair status` shows the redemption `failed`:** fix the network, then repeat
  steps 2 to 5 with a new code.
- **A paired workspace shows `last attempt failed`:** the reason and time are
  beside it. If the other side now has another address, use
  [`/pair address`](#if-as-address-changed) on the side that dials.

## If A's address changed

The ⇄ line on B says which address it dials: `this side dials A_IP:7445`.
When A gets another address (a new DHCP lease, another network), B keeps
dialling the old one, and messages stay queued; `/pair status` shows
`last attempt failed`. In A's `⇄` tab on B:

```text
/pair address NEW_A_IP:7445
```

B dials the new address and checks that the workspace there presents A's
identity key; nothing is exchanged. Only if it does is the address stored:
the contact, its name, its grants and the history stay, B drops any link to
the old address and syncs there from then on. A refusal (unreachable, or
another workspace's identity) leaves the old address. Only the side that
dials keeps the other's address; on A, the command says B must update it.

Pairing again from B to the new address (steps 2 to 5) works too. Either
way, only a newer change moves the address, so one finishing late never
moves it back.

## Change or stop

To stop traffic with one paired workspace for a while, open its `⇄` tab:

```text
/pair pause      block new traffic with it, both ways
/pair resume     let queued messages flow again
```

Pausing keeps the workspace, what you share with it and the history. While
it is paused, this side neither dials it nor answers it, and they see their
attempts refused.

- A message already on its way when you pause may still finish arriving.
  No new incoming traffic is accepted while paused.
- Received messages and review requests that haven't reached your agents
  yet wait until you resume. Work already delivered to agents may go on.
- Anything you write waits too: the tab keeps it as a draft.
- Reviews already running here still finish, and their results leave once
  you resume.
- Messages already queued keep their usual expiry.

From a terminal, in `secured`:

```sh
pairtask operator peer pause sha256:…
pairtask operator peer resume sha256:…
```

To stop pairing altogether, or change how this machine listens:

```sh
pairtask pairing on --host-port 8445   # another port (redeem with A_IP:8445)
pairtask pairing on --host-port 0      # stay paired, stop listening
pairtask pairing off                   # turn pairing off
```

`on` and `off` restart the containers. If the port is taken, the change fails
and the requested port is kept: free the port and run `pairing on` again, or
pick another. Commands and limits: [host commands](host-commands.md#inside-the-sandbox),
[operator chat](operator-chat.md).
