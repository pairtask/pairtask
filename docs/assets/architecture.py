"""Write docs/assets/architecture.svg, the README's architecture diagram.

    python3 docs/assets/architecture.py

Placed by hand on a 1600 x 900 grid with the helpers in diagram.py.
"""

from pathlib import Path

from diagram import BOX, PEER, Svg

OUT = Path(__file__).with_name("architecture.svg")

FLOWS = {
    "you": "#008080",  # you, through a terminal
    "message": "#58A824",  # messages through the broker
    "pairing": "#8A2BE2",  # pairing between workspaces
    "storage": "#4B0082",  # the broker's database
}


def generate():
    s = Svg(FLOWS)
    s.start("pairtask", "Agents and your chat on one machine · a paired workspace on another")

    # You, above the secured column.
    s.box(495, 150, 360, 95, "You", "host terminals")

    # Your machine: workspace | secured | connector.
    s.boundary(30, 275, 1155, 530, "Your machine")
    s.boundary(55, 335, 400, 450, "workspace", "network by default")
    s.boundary(495, 335, 360, 450, "secured", "offline: no network")
    s.boundary(895, 335, 270, 450, "connector", "only with pairing on")

    # Row A: agents and the chat; row B: dashboard, broker, relay; row C: stores.
    s.box(80, 415, 350, 95, "Agents", "Codex · Claude Code · Pi")
    s.box(520, 415, 310, 95, "Operator chat", "pairtask chat")
    s.box(80, 545, 350, 95, "Dashboard", "read-only status")
    s.box(520, 545, 310, 95, "Broker", "messages · pairing")
    s.box(915, 545, 230, 95, "Relay", "holds no key")
    s.store(80, 670, 350, 100, "Projects · agent home", "survive upgrades")
    s.store(520, 670, 310, 100, "SQLite", "only the broker uses it")

    # Another machine.
    s.boundary(1290, 275, 280, 530, "Another machine")
    s.box(1310, 545, 240, 95, "Its broker", "paired workspace", PEER)

    # You start agents and open the chat.
    s.arrow("M495 197 H255 V415", "you")
    s.text(275, 322, "pairtask start", "label")
    s.arrow("M745 245 V415", "you")
    s.text(765, 322, "pairtask chat", "label")

    # Agents and the dashboard reach the broker through one shared socket.
    s.arrow("M430 462 H470 V575 H520", "message")
    s.text(485, 530, "broker.sock", "label")
    s.arrow("M430 605 H520", "message")
    s.text(475, 597, "status", "label", "middle")
    s.port(520, 575)
    s.port(520, 605)

    # The chat has its own operator socket; only the broker touches SQLite.
    s.arrow("M675 510 V545", "you")
    s.text(695, 534, "operator socket", "label")
    s.arrow("M675 640 V670", "storage")
    s.text(695, 662, "read / write", "label")

    # Pairing: broker ⇄ relay ⇄ the other broker, TLS end to end.
    s.arrow("M840 592 H915", "pairing", both=True)
    s.port(830, 592)
    s.arrow("M1145 592 H1310", "pairing", both=True)
    s.text(1227, 577, "TCP 7445", "label", "middle")
    s.text(1227, 622, "LAN or tailnet", "label", "middle")

    # Footnotes in place of a legend.
    s.legend(40, 838, [("you", "you, through a terminal"), ("message", "messages through the broker"),
                       ("pairing", "pairing between workspaces"), ("storage", "storage"), ("socket", "shared socket")])
    s.text(40, 872, "Pairing runs TLS end to end between the brokers, pinned to each workspace's identity key;", "small")
    s.text(40, 896, "the relay forwards sealed connections and sees no content.", "small")

    s.save(OUT)


if __name__ == "__main__":
    generate()
