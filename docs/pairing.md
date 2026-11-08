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

The tab's card shows what you share and what they share. A change reaches
their tab as soon as the two sides are connected, with no message needed; while
they aren't, it goes at the next connection. To write to one of their agents,
start with `@` and its name:

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
Nothing written in a `⇄` tab reaches your own agents unless you tag them
`@AGENT@here`: then they get it too, with the conversation named, and a tagged
agent's reply goes back to the same people there (#278). The line above the
draft says who gets it, and a draft starting with one of your agents they
don't share, untagged, is held with where to send it instead.

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
allowed is refused, and the `⇄` tab shows it ("not open to review requests
from this workspace"), so you know it was asked for.
Allowing the agent afterwards only lets them ask again: they have to send a
new request. A request waiting for you shows in
the `⇄` tab: `/pair approve ID` or `/pair decline ID`. `/pair status` also
counts them per workspace under **Waiting for you**, so you see them without
opening each tab. Undecided ones expire after 7 days.

To ask one of their agents, name it with the workspace, from any tab:

```text
/review OWNER/REPO#12 @codex-1@WORKSPACE [what to focus on]
/review https://github.com/OWNER/REPO/pull/12 @codex-1@WORKSPACE
```

- Your side looks up the change's current base and head with your own GitHub
  access (`GH_TOKEN`, `GITHUB_TOKEN` or `gh auth login`), pins them and sends.
  The status line says which commits were pinned; nothing of your access goes
  to the other workspace.
- The reviewer reviews exactly those commits: if the pull request moved on,
  the review can't start.
- `--pin BASE..HEAD` pins them yourself (full commit IDs) and skips the lookup.
- If the pull request moves before they accept it, their side declines it as
  moved and the request's line says where to, as they reported it. Ask again
  with `--replaces ASK_ID`, which links the new request to the old one: your
  own final request to the same agent and change, replaced once. Pins are
  never moved for you.
- Nothing is published unless you ask: `--publish`, or `--formal` for a formal
  approval or change request. They allow at most what they granted.
- WORKSPACE is what you call it, the name it gives itself, or its key (at least
  `sha256:` and 12 hex digits). A name more than one workspace answers to is
  refused; completion offers each one a name that is its alone. Quote one with
  a space: `"@codex-1@Home Office"`. An agent whose ID has a quote or a
  backslash isn't offered and can't be named here: use `/pair review` in the
  workspace's `⇄` tab.
- Esc, another `/review` or another tab cancels a lookup; nothing is sent.

In the workspace's `⇄` tab, `/pair review codex-1 OWNER/REPO#12 BASE_SHA HEAD_SHA
[what to focus on] [--publish] [--formal]` does the same with the commits given.
An agent asks with its `request_review` tool.

Where the request stands comes back to you: waiting for their operator,
declined, accepted, the verdict with its summary, and the published link.
The `⇄` tab shows it on your request; an agent that asked gets messages
with the latest status or result (steps in quick succession come as one),
and its `reviews_asked` tool lists its requests. A request refused there for
good (their operator hasn't allowed that agent: `not_shared`) is told to the
agent that sent it too, with what lets it ask again. Each request names its ID
on both sides. On the reviewer's side, the request shows the requester's
ask ID and, once it is a review there, the local review ID. On your side,
it shows their review ID once they report it, or "reviewer ID not
reported" until then. Nothing is reported before the request becomes a
review there, or by a pairtask that predates review IDs.

The verdict's findings come back with it, even when nothing is published
(#303). Up to 24 travel, each summary cut at 512 bytes if longer; the rest
stay on the reviewer's side, and the count says how many. They stay with your
request after the reviewer is unshared, for as long as the request is kept.
The review card shows the first three, `/reviews` counts them, and
`/reviews findings aN` shows them all. An agent gets the first three in its
notice, and all of them from `reviews_asked` with the request's ask ID and
workspace. Both sides need a pairtask
with findings (peer messaging v7); otherwise the verdict comes back without
them.

## 9. Decide requests you never decided

By default a paired workspace's message or review request for something you
never allowed is refused. Turn on consent prompts for that workspace and such
requests wait for your decision instead:

```text
/pair consent on Office      hold its undecided requests for you
/pair consent off Office     refuse them again, as before
/pair consent                who has requests waiting, one line each
/pair consent contact:Office list Office's waiting requests
/pair consent ID             open one waiting request
```

`NAME` is what `/pair status` calls the workspace, or the start of its key
when two share a name; a name that matches no workspace, or more than one, is
refused. A waiting request shows under Needs you in `/reviews`, in
`/pair status` and in the footer, with the command that opens it. Nothing
opens on its own and your draft stays as it was.

In the list, ↑/↓ highlight a request and Enter shows it in full: who sent it
(the workspace's key, apart from the name it claims), the agents it is for,
what it asks of each, and when it expires. Nothing is chosen at first. Press
1 to allow this request only, 2 to allow it for this session, 3 to always
allow exactly this, 4 to decline and stop asking for it, or 5 to decide later;
then Ctrl+Y confirms. Enter, Tab and paste never choose or confirm, and Esc
closes without deciding. If the request changes, or the connection is
refreshed, the choice is dropped and you choose again on what is shown now.

What you turned off before, or declined, stays denied. To ask again for one
agent's operation, reset it to undecided:

```text
/pair consent reset Office claude-1 review
```

The operation is `message`, `review` or `publish`. `/pair consent reset
Office` or `/pair consent reset Office claude-1` sends nothing: it lists the
scopes you can reset and the exact command for each. A scope shows as
allowed when the workspace is shared or granted it; otherwise its state is
unavailable, since the broker doesn't say whether it is denied or undecided. A
workspace paired before consent prompts existed treats everything you never
decided as denied until you reset it this way. A broker without consent
prompts says `consent prompts need a newer broker` and nothing is sent.

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
  Change it only with `pairtask pairing on --name` or a pairtask upgrade:
  they keep the secured broker down until the new name is in place, so it
  never pairs under the old one. A direct `tofu apply` doesn't, and can
  start it under the old name until the next pairtask rollout.
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

## Moving between networks

`pairtask pairing on` also starts a small helper on each machine, a user
service (no root): a launchd agent on macOS, `systemd --user` on Linux. It
lets a paired workspace find the other again after an address change,
without `/pair address`. It does nothing unless the secured broker asks:

- **Listening side:** while a paired workspace dials this one, the helper
  announces on the local network (mDNS, `_pairtask._tcp.local`) that a
  pairtask listener is at this machine's addresses, on the published
  `--host-port`. The announcement is presence and port only, under a random
  name that changes each time the helper starts. It carries no workspace or
  contact name, no key and no fingerprint. Anyone on the network can still
  see that a pairtask listener is there.
- **Dialling side:** while a workspace this side dials shows
  `last attempt failed`, the helper looks for those announcements. With
  Tailscale installed, it also takes each online peer's MagicDNS name and
  Tailscale addresses from `tailscale status`, crossed with the ports the
  failing workspaces were last reached on. It hands the broker at most 8
  endpoints at a time, Tailscale names first, then addresses, then the
  local network's, and at most once every 30 seconds.

The broker alone decides. It tries each endpoint as a dial would. It moves a
workspace to an endpoint only when the workspace there presents that
workspace's pinned identity, and it checks the identity before it shows its
own. It then keeps everything as `/pair address` does. A working address is
never replaced. Paused and unpaired workspaces are never matched.

`pairtask discovery status` shows what the helper last did: the broker's
answer, what it announced and the outcome for each endpoint it handed over.
`pairtask discovery run --once` runs one round in the foreground.

**Tailscale names come first.** A configured MagicDNS name follows the
other machine wherever it goes, so it is tried before anything else. Whether
the broker's side can resolve it depends on the deployment's DNS (on macOS,
the Colima VM's), which isn't the host's. When it can't, the Tailscale
addresses are tried next. The local-network announcement helps only when
both machines are on the same network.

**Not covered:** both machines move to different networks, with no stable
name and no tailnet between them. Neither can find the other, and the
workspace keeps showing `last attempt failed` until you use `/pair address`
or pair again. Windows, native or with WSL2, is not a supported host, and
discovery there is unverified.

To turn the helper off, and keep it off through later upgrades:

```sh
pairtask pairing on --no-discovery   # --discovery turns it back on
```

`pairtask pairing off` stops and removes it, and `pairtask upgrade` restarts
it. Pairing works without it; only automatic recovery is lost.

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

### Unpair

To end a pairing for good, open its `⇄` tab and type `/pair unpair`, or name
it from any tab: `/pair unpair NAME`. A card says what ends:

- what you share with it, review grants and consent end;
- queued messages expire, shown `not sent`, and held requests end;
- reviews running here finish without sending their results.

To go ahead, type the line the card shows, `/pair unpair confirm` and the
first 8 characters of its key after `sha256:`. A paste, Enter, Esc, another
tab, a reconnect or any change to that workspace in the pairing state
discards the card, and nothing is sent. One card sends one unpair. If the
connection drops before the answer, the chat sends the same unpair again
once it reconnects, and the broker answers it with the first result.

The workspace keeps a read-only tab, `⇄ NAME (unpaired)`, after a restart
too. Its history, what was never sent and the reads stay. A draft stays as
you wrote it, but nothing is sent, acknowledged, shared or changed there.
`/pair status` shows `unpaired by you DATE · history kept`.

From a terminal, by the full key only:

```sh
pairtask operator peer unpair sha256:FULL --confirm sha256:FULL
pairtask operator peer messages sha256:FULL    # the history, still
```

Without `--epoch N` the command reads the current pairing first and echoes
it. `--message-id ID` repeats an unpair safely: the retry answers the first
result.

Pairing again needs a fresh code (steps 2 to 5). Its request shows
`re-pair`.

### What the other side sees

This side stops at once; the other side learns from a notice. Its card and
`/pair status` say `unpaired by them`, or `paused by them`. Here, the card
says how the notice stands:

- `notice pending: they learn when they next connect`, while it waits;
- `notice undelivered` after five attempts in total.

A side that only waits to be dialed has no address for the other one. Its
notice waits until the other side connects again.
`their state: unavailable (older version)` means the other side runs a
pairtask without notices.

### Repair

If the other side still holds another pairing with this one, the card says
`holds another pairing: /pair invite --repair`. Pair again and declare which
workspace the pairing repairs:

```text
/pair invite --repair NAME                  on one side: opens a code
/pair redeem --repair NAME CODE@HOST:PORT   on the other: redeems it
```

NAME must name exactly one paired workspace, by its name or key; otherwise
nothing is sent, so type more of the key. From a terminal, use
`pairtask operator pairing open --repair KEY` and `redeem --repair KEY`.
The request shows `repair`. Pairing again without `--repair` with a
workspace that is still paired only moves its address (`address move`).

Changes that cross are refused, never merged:

- If both sides change the pairing at once, the later change is refused
  (`pairing_crossed`); make it again.
- If two pairings could both be meant, both are refused
  (`pairing_ambiguous`); start one fresh repair.

### Versions

Unpairing always revokes on this side, whatever version the other side
runs. Telling them, pairing again and repairing need peer protocol v9 on
both sides:

- A workspace paired under v9 refuses a peer that still runs v8. The card
  says `unsupported: the other side must update`.
- A pairing made with a v8 peer keeps working with either version, and can
  be unpaired here; the other side isn't notified.
- Pairing again, or repairing, needs v9 on both sides.

**Release note.** Downgrading to a build without peer protocol v9 support
is unsupported: the upgrade
migrates the broker's database, and a v9 pairing refuses an older peer.
Upgrade the other side instead. Queued messages wait for it until they
expire.

### Stop pairing

To stop pairing on this machine altogether, or change how it listens:

```sh
pairtask pairing on --host-port 8445   # another port (redeem with A_IP:8445)
pairtask pairing on --host-port 0      # stay paired, stop listening
pairtask pairing off                   # turn pairing off
```

`on` and `off` restart the containers. If the port is taken, the change fails
and the requested port is kept: free the port and run `pairing on` again, or
pick another. Commands and limits: [host commands](host-commands.md#inside-the-sandbox),
[operator chat](operator-chat.md).
