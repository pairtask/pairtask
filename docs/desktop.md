# Join from Claude Desktop

Someone who works in Claude Desktop, without a terminal or a pairtask
workspace, can pair with yours and message you and the agents you share.
They install the pairtask extension. It runs a light pairtask node on their
computer that redeems your invitation, then exchanges messages with your
workspace.

This is a preview. It supports macOS and Linux; Windows isn't supported yet.

Call the two sides **A**, your full workspace, which invites, and **D**, the
Claude Desktop user, who joins.

## 1. On A: open an invitation

Turn pairing on if it isn't (see [Pair two machines](pairing.md#1-on-both-machines-update-and-turn-pairing-on)),
then in `pairtask chat`:

```text
/pair open
```

Give D's user three things over a channel you trust: A's address as
`HOST:PORT` (the address D can reach, and the pairing port), the code, and
your name. The code works once and expires.

## 2. On D: install the extension

Download the bundle for the computer from the latest
[release](https://github.com/pairtask/pairtask/releases/latest):
`pairtask-desktop-darwin-arm64.mcpb` for an Apple silicon Mac,
`pairtask-desktop-darwin-amd64.mcpb` for an Intel Mac, or the `linux` one
for the machine's architecture. Then, in Claude Desktop:

1. **Settings → Desktop app**, then **Manage** next to **Extensions**.
2. **Advanced settings**, then under **Extension Developer**, **Install
   Extension…**
3. Select the `.mcpb` file and follow the prompts. Desktop asks for **Your
   name**: the name A's operator will see.

These are the steps in Anthropic's
[guide to local MCP servers](https://support.claude.com/en/articles/10949351-getting-started-with-local-mcp-servers-on-claude-desktop).

## 3. On D: join

Ask Claude in Desktop, for example:

> Join the pairtask workspace at 192.168.1.20:7445 with code 123456.

Claude calls `pair_redeem`. Then ask it for this computer's pairtask key
(`whoami`), and read the first characters after `sha256:` to A's operator.

## 4. On A: check the key, then accept

As for any pairing: type `/pair accept `, pick the request whose key
starts with what D read out, and press Enter
([details](pairing.md#4-check-it-is-really-b-then-accept-on-a)). D's node
completes the pairing by itself. Ask Claude for the pairing status
(`pair_status`) until A shows up as a confirmed contact.

## 5. Talk

On D, ask Claude to message A's operator, or one of the agents A shares
with D:

> Send "can you look at the failing test?" to Simon's workspace.
>
> Check my pairtask inbox.

On A, D's messages arrive in D's tab in `pairtask chat`, like any paired
workspace's. To let D write to your agents, share them with D
([Let agents talk across](pairing.md#7-let-agents-talk-across)).

## What to know

- **Checking for messages.** New messages don't pop up in Desktop. Claude
  sees them only when it checks the inbox (`check_inbox`), so ask it to.
- **Desktop has to be running.** D's node runs only while Claude Desktop
  does. A's messages wait at A until D's node next connects.
- **Nothing listens.** D's node never accepts connections. It only dials
  the workspaces it joined, so D can't open invitations.
- **Replies sent from Desktop aren't threaded yet.** An answer from D is a
  new message in the same conversation. A's agents' replies to D are
  threaded.
- **What it keeps.** D's key and message history stay in a private directory.
  On macOS that's `~/Library/Application Support/pairtask-desktop`; on
  Linux, `~/.config/pairtask-desktop`. Removing the extension leaves it;
  delete that directory to forget the pairing and the key.
- **Message text is data.** Claude treats what arrives from another
  workspace as information, not as instructions from you.

## Build the bundle yourself

From a checkout, with Go and Python 3:

```sh
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o dist/pairtask-desktop ./cmd/pairtask-desktop
python3 scripts/build-mcpb.py --binary dist/pairtask-desktop --goos darwin --goarch arm64 --version 0.1.0 --out dist
```

`build-mcpb.py` refuses a binary built for a platform other than the one
named.
