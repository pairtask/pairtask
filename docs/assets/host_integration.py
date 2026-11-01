"""Write docs/assets/host-integration.svg: how agents running directly on your
host reach the broker.

    python3 docs/assets/host_integration.py

Placed by hand on a 1600 x 900 grid with the helpers in diagram.py: three
lanes, one per way of connecting, all ending at the broker socket.
"""

from pathlib import Path

from diagram import Svg

OUT = Path(__file__).with_name("host-integration.svg")

FLOWS = {
    "you": "#008080",  # you, through a terminal
    "session": "#B65C13",  # the agent and its adapter
    "message": "#58A824",  # messages through the broker
    "storage": "#4B0082",  # the broker's database
}

# Each lane: its x, width, heading and note, then the agent and the adapter.
LANES = [
    (55, 490, "Managed Codex", "listens while it runs",
     ("Codex terminal UI", "you prompt and approve"),
     ("pairtask-codex", "proxy, listener, App Server"), "private socket"),
    (575, 450, "Claude channel", "listens while it runs",
     ("Claude Code", "channel opt-in, org policy"),
     ("pairtask-mcp", "--claude-channel --auto-listen"), "MCP · stdio"),
    (1055, 490, "Plain MCP", "checks its inbox when asked",
     ("Codex or Claude Code", "any MCP client"),
     ("pairtask-mcp", "default mode"), "MCP · stdio"),
]


def generate():
    s = Svg(FLOWS)
    s.start("Agents on your host", "Three ways an agent reaches the broker")

    s.box(620, 118, 360, 90, "You", "a terminal per agent")

    for x, w, heading, note, agent, adapter, link in LANES:
        s.boundary(x, 260, w, 325, heading, note)
        bx, bw = x + 25, w - 50
        mid = bx + bw // 2
        s.box(bx, 335, bw, 90, *agent)
        s.box(bx, 470, bw, 90, *adapter)
        s.arrow("M%s 425 V470" % mid, "session", both=True)
        s.text(mid + 20, 455, link, "label")

    # You launch each agent and work in its terminal.
    s.arrow("M900 208 V335", "you")
    s.arrow("M620 163 H420 V335", "you")
    s.arrow("M980 163 H1420 V335", "you")
    s.text(920, 244, "launch, prompt, approve", "label")

    # Every adapter speaks the broker protocol over one Unix socket.
    s.box(620, 610, 360, 90, "Broker socket", "/tmp/collab-ai.sock")
    s.arrow("M800 560 V610", "message")
    s.arrow("M300 560 V655 H620", "message")
    s.arrow("M1300 560 V655 H980", "message")
    s.text(460, 643, "broker protocol", "label", "middle")

    s.box(620, 735, 360, 90, "Broker", "one owner per inbox")
    s.arrow("M800 700 V735", "message")
    s.store(1040, 730, 300, 100, "SQLite", "messages, receipts")
    s.arrow("M980 780 H1040", "storage")

    s.legend(960, 98, [("you", "you"), ("session", "agent and adapter"), ("message", "broker protocol")])
    s.text(40, 866, "Managed Codex and the Claude channel deliver messages while their process runs; plain MCP sees them only when the agent calls a tool.", "small")
    s.text(40, 892, "Each agent has its own durable inbox. pairtask status and pairtask dashboard read the same socket, read-only.", "small")

    s.save(OUT)


if __name__ == "__main__":
    generate()
