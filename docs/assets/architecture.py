"""Write docs/assets/architecture.svg, the README's architecture diagram.

    python3 docs/assets/architecture.py

The layout is placed by hand on a 1600 x 900 grid, so every arrow runs
straight across or straight down and the columns line up: boundaries are
dashed, components are light boxes with a heading and one line, stores
are cylinders, and arrows take one color per kind of flow, labelled on a
white halo. Render it and look at it after any change; nothing checks for
overlaps but the eye.
"""

from html import escape
from pathlib import Path

OUT = Path(__file__).with_name("architecture.svg")

FLOWS = {
    "you": "#008080",  # you, through a terminal
    "message": "#58A824",  # messages through the broker
    "pairing": "#8A2BE2",  # pairing between workspaces
    "storage": "#4B0082",  # the broker's database
}

BOX = "#F6F6F6"
STORE = "#90EE90"
PEER = "#ADD8E6"
SOCKET = "#F4B6D2"

STYLE = (
    "text{font-family:Arial,Helvetica,sans-serif;fill:#20252B}"
    ".title{font-size:38px;font-weight:700}"
    ".heading{font-size:27px;font-weight:700}"
    ".body{font-size:23px}"
    ".small{font-size:19px;fill:#4D555E}"
    ".label{font-size:21px;paint-order:stroke;stroke:white;stroke-width:7px;stroke-linejoin:round}"
)


class Svg:
    def __init__(self):
        self.parts = []

    def start(self, title, subtitle):
        self.parts.append(
            '<svg xmlns="http://www.w3.org/2000/svg" width="1920" height="1080" '
            'viewBox="0 0 1600 900" role="img" aria-labelledby="title">'
        )
        self.parts.append('<title id="title">' + escape(title) + "</title><defs>")
        for kind, color in FLOWS.items():
            self.parts.append(
                '<marker id="' + kind + '" markerWidth="12" markerHeight="12" refX="10" refY="6" '
                'orient="auto-start-reverse" markerUnits="userSpaceOnUse">'
                '<path d="M0 0 L12 6 L0 12 Z" fill="' + color + '"/></marker>'
            )
        self.parts.append("</defs><style>" + STYLE + "</style>")
        self.parts.append('<rect width="1600" height="900" fill="white"/>')
        self.text(40, 58, title, "title")
        self.text(40, 98, subtitle)

    def text(self, x, y, value, cls="body", anchor="start"):
        self.parts.append(
            '<text x="%s" y="%s" class="%s" text-anchor="%s">%s</text>' % (x, y, cls, anchor, escape(value))
        )

    def rect(self, x, y, w, h, fill="white", dashed=False):
        dash = ' stroke-dasharray="8 6"' if dashed else ""
        self.parts.append(
            '<rect x="%s" y="%s" width="%s" height="%s" rx="4" fill="%s" stroke="#888888" stroke-width="2"%s/>'
            % (x, y, w, h, fill, dash)
        )

    def boundary(self, x, y, w, h, heading, note=""):
        self.rect(x, y, w, h, dashed=True)
        self.text(x + 25, y + 39, heading, "heading")
        if note:
            self.text(x + 25, y + 69, note, "small")

    def box(self, x, y, w, h, heading, body, fill=BOX):
        self.rect(x, y, w, h, fill)
        self.text(x + 22, y + 40, heading, "heading")
        self.text(x + 22, y + 76, body)

    def store(self, x, y, w, h, heading, body, fill=STORE):
        rx, ry = w / 2, 14
        self.parts.append(
            '<path d="M%s %s A%s %s 0 0 1 %s %s V%s A%s %s 0 0 1 %s %s Z" fill="%s" stroke="#888888" stroke-width="2"/>'
            % (x, y + ry, rx, ry, x + w, y + ry, y + h - ry, rx, ry, x, y + h - ry, fill)
        )
        self.parts.append(
            '<ellipse cx="%s" cy="%s" rx="%s" ry="%s" fill="%s" stroke="#888888" stroke-width="2"/>'
            % (x + rx, y + ry, rx, ry, fill)
        )
        self.text(x + 22, y + 48, heading, "heading")
        self.text(x + 22, y + 80, body)

    def port(self, x, y):
        """A shared socket: a small square where an arrow meets a box."""
        self.parts.append(
            '<rect x="%s" y="%s" width="16" height="16" fill="%s" stroke="#888888" stroke-width="2"/>'
            % (x - 8, y - 8, SOCKET)
        )

    def arrow(self, path, kind, both=False):
        start = ' marker-start="url(#%s)"' % kind if both else ""
        self.parts.append(
            '<path d="%s" fill="none" stroke="%s" stroke-width="3.5" marker-end="url(#%s)"%s/>'
            % (path, FLOWS[kind], kind, start)
        )

    def save(self, path):
        self.parts.append("</svg>")
        path.write_text("\n".join(self.parts) + "\n")


def generate():
    s = Svg()
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
    s.text(40, 832, "Green: messages through the broker · teal: you · violet: pairing · indigo: storage.", "small")
    s.text(40, 858, "Pairing runs TLS end to end between the brokers, pinned to each workspace's identity key;", "small")
    s.text(40, 884, "the relay forwards sealed connections and sees no content. Pink squares are shared sockets.", "small")

    s.save(OUT)


if __name__ == "__main__":
    generate()
