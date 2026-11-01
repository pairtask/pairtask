"""Shared drawing helpers for the diagrams in docs/assets.

Each diagram is a small script that places everything on a grid by hand, so
arrows run straight across or straight down and columns line up:
boundaries are dashed, components are light boxes with a heading and one
line, stores are cylinders, and each kind of flow has its own arrow color,
labelled on a white halo. Footnotes take the place of a legend. Render a
diagram and look at it after any change; nothing checks for overlaps but
the eye.
"""

from html import escape

BOX = "#F6F6F6"
STORE = "#90EE90"
PEER = "#ADD8E6"
OPTIONAL = "#FFF3E5"
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
    """An SVG built in order: start, then shapes, then save."""

    def __init__(self, flows, width=1600, height=900):
        self.flows = flows
        self.width, self.height = width, height
        self.parts = []

    def start(self, title, subtitle=""):
        w, h = self.width, self.height
        self.parts.append(
            '<svg xmlns="http://www.w3.org/2000/svg" width="%s" height="%s" '
            'viewBox="0 0 %s %s" role="img" aria-labelledby="title">' % (w * 6 // 5, h * 6 // 5, w, h)
        )
        self.parts.append('<title id="title">' + escape(title) + "</title><defs>")
        # One marker per flow for the end, and a mirrored one for the start
        # of a two-way arrow: plain orient="auto" renders the same everywhere.
        for kind, color in self.flows.items():
            for suffix, ref, tip in (("", 10, "M0 0 L12 6 L0 12 Z"), ("-start", 2, "M12 0 L0 6 L12 12 Z")):
                self.parts.append(
                    '<marker id="%s%s" markerWidth="12" markerHeight="12" refX="%s" refY="6" '
                    'orient="auto" markerUnits="userSpaceOnUse"><path d="%s" fill="%s"/></marker>'
                    % (kind, suffix, ref, tip, color)
                )
        self.parts.append("</defs><style>" + STYLE + "</style>")
        self.parts.append('<rect width="%s" height="%s" fill="white"/>' % (w, h))
        self.text(40, 58, title, "title")
        if subtitle:
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
        start = ' marker-start="url(#%s-start)"' % kind if both else ""
        self.parts.append(
            '<path d="%s" fill="none" stroke="%s" stroke-width="3.5" marker-end="url(#%s)"%s/>'
            % (path, self.flows[kind], kind, start)
        )

    def legend(self, x, y, entries):
        """One row naming each flow: a short arrow in its color, then its
        name; the entry "socket" draws the shared-socket square instead."""
        for kind, name in entries:
            if kind == "socket":
                self.port(x + 20, y - 7)
            else:
                self.arrow("M%s %s H%s" % (x, y - 7, x + 40), kind)
            self.text(x + 52, y, name, "small")
            x += 52 + 10 * len(name) + 36

    def save(self, path):
        self.parts.append("</svg>")
        path.write_text("\n".join(self.parts) + "\n")
