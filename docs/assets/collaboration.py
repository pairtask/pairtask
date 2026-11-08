"""Write the README's hero diagram: light and dark, wide and narrow.

    python3 docs/assets/collaboration.py [OUT_DIR]

The wide picture puts the two panels side by side (1400 x 600) for a desktop
README column of about 830 px; the narrow one stacks them (920 x 1080) with
larger type for phone widths, and the README's <picture> picks it by width.

Two panels, the same personal unit in both: a person directing two agents
that message each other and review each other's changes; then that unit
paired with a colleague's. The paired link is drawn as the logo's two arcs
meeting. Placed by hand on a 1600 x 760 grid; render and look after any
change.
"""

import sys
from html import escape
from pathlib import Path

GREEN, DARK = "#0E9F6E", "#3B4349"  # the logo's two strokes
PANEL_W, PANEL_H = 400, 380  # one person's machine

THEMES = {
    # The banner's palette: light #F3F4F1 / #3B4349, dark #1E2327 / #B9BFC4, green #0E9F6E.
    "light": {"bg": "#F3F4F1", "panel": "#FFFFFF", "text": DARK, "muted": "#5B646D", "line": "#9AA2AA",
              "agent": "#F6F6F6", "link": DARK},
    "dark": {"bg": "#1E2327", "panel": "#262C31", "text": "#FFFFFF", "muted": "#B9BFC4", "line": "#6B747D",
             "agent": "#2E353B", "link": "#B9BFC4"},
}


class Svg:
    def __init__(self, theme, width, height, type_scale=1.0):
        self.t, self.w, self.h, self.parts = theme, width, height, []
        h, b, sm, c = (round(px * type_scale) for px in (34, 28, 26, 28))
        self.parts.append(
            '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" role="img" '
            'aria-labelledby="title desc">' % (width, height, width, height))
        self.parts.append("<title id=\"title\">pairtask: connecting agents</title>")
        self.parts.append("<desc id=\"desc\">You direct two agents on your machine. Pair with a colleague's machine so "
                          "your agents and theirs can collaborate. Each person chooses which agents to share.</desc>")
        # A tip for the end of an arrow and a mirrored one for the start of a two-way arrow.
        self.parts.append("<defs><marker id=\"tip\" markerWidth=\"10\" markerHeight=\"10\" refX=\"8\" refY=\"5\" "
                          "orient=\"auto\" markerUnits=\"userSpaceOnUse\"><path d=\"M0 0 L10 5 L0 10 Z\" fill=\"%s\"/>"
                          "</marker><marker id=\"tip-start\" markerWidth=\"10\" markerHeight=\"10\" refX=\"2\" refY=\"5\" "
                          "orient=\"auto\" markerUnits=\"userSpaceOnUse\"><path d=\"M10 0 L0 5 L10 10 Z\" fill=\"%s\"/>"
                          "</marker></defs>" % (theme["line"], theme["line"]))
        self.parts.append("<style>text{font-family:Arial,Helvetica,sans-serif;fill:%s}.h{font-size:%dpx;font-weight:700}"
                          ".b{font-size:%dpx}.s{font-size:%dpx;fill:%s}.c{font-size:%dpx;fill:%s}</style>"
                          % (theme["text"], h, b, sm, theme["muted"], c, theme["muted"]))
        self.parts.append('<rect width="%d" height="%d" fill="%s"/>' % (width, height, theme["bg"]))

    def text(self, x, y, value, cls="b", anchor="middle"):
        self.parts.append('<text x="%s" y="%s" class="%s" text-anchor="%s">%s</text>' % (x, y, cls, anchor, escape(value)))

    def panel(self, x, y, heading):
        self.parts.append('<rect x="%s" y="%s" width="%s" height="%s" rx="18" fill="%s" stroke="%s" stroke-width="2" '
                          'stroke-dasharray="10 8"/>' % (x, y, PANEL_W, PANEL_H, self.t["panel"], self.t["line"]))
        self.text(x + 28, y + 44, heading, "h", "start")

    def person(self, cx, cy, label):
        self.parts.append('<circle cx="%s" cy="%s" r="20" fill="none" stroke="%s" stroke-width="9"/>' % (cx, cy, GREEN))
        self.parts.append('<path d="M%s %s Q%s %s %s %s" fill="none" stroke="%s" stroke-width="9" stroke-linecap="round"/>'
                          % (cx - 40, cy + 70, cx, cy + 28, cx + 40, cy + 70, GREEN))
        self.text(cx + 56, cy + 10, label, "b", "start")

    def agent(self, x, y, label):
        self.parts.append('<rect x="%s" y="%s" width="150" height="64" rx="14" fill="%s" stroke="%s" stroke-width="2"/>'
                          % (x, y, self.t["agent"], self.t["line"]))
        self.text(x + 75, y + 42, label)

    def arrow(self, path, both=False):
        start = ' marker-start="url(#tip-start)"' if both else ""
        self.parts.append('<path d="%s" fill="none" stroke="%s" stroke-width="3" marker-end="url(#tip)"%s/>'
                          % (path, self.t["line"], start))

    def unit(self, x, y, who, machine):
        """One person directing two agents on one machine, 400 wide; returns the agent row's y."""
        self.panel(x, y, machine)
        mid, row = x + 200, y + 250
        self.person(x + 110, y + 100, who)
        # You reach the whole team: one arrow down to a bar across both agents.
        self.arrow("M%s %s V%s" % (x + 110, y + 180, row - 28))
        self.text(x + 124, y + 215, "direct", "s", "start")
        self.parts.append('<path d="M%s %s H%s M%s %s V%s M%s %s V%s" fill="none" stroke="%s" stroke-width="3"/>'
                          % (x + 110, row - 16, x + 290, x + 110, row - 16, row, x + 290, row - 16, row, self.t["line"]))
        self.agent(x + 35, row, "Agent A")
        self.agent(x + 215, row, "Agent B")
        self.arrow("M%s %s H%s" % (x + 188, row + 32, x + 212), both=True)
        self.text(mid, row + 100, "messages · reviews", "s")
        return row

    def handshake(self, x1, x2, y):
        """The logo's two arcs meeting: the paired link between two agent rows."""
        mid = (x1 + x2) // 2
        self.parts.append('<path d="M%s %s Q%s %s %s %s" fill="none" stroke="%s" stroke-width="9" stroke-linecap="round"/>'
                          % (x1, y, (x1 + mid) // 2, y + 46, mid, y + 46, GREEN))
        self.parts.append('<path d="M%s %s Q%s %s %s %s" fill="none" stroke="%s" stroke-width="9" stroke-linecap="round"/>'
                          % (mid, y + 46, (mid + x2) // 2, y + 46, x2, y, self.t["link"]))
        self.text(mid, y + 90, "paired", "s")

    def save(self, path):
        self.parts.append("</svg>")
        Path(path).write_text("\n".join(self.parts) + "\n")


def generate(theme, layout):
    if layout == "narrow":
        return generate_narrow(theme)
    s = Svg(THEMES[theme], 1400, 600)
    # Left: one person, one machine.
    s.text(40, 60, "One person · one machine", "h", "start")
    s.unit(40, 90, "You", "Your machine")
    # Right: two people, two machines, one direct link; 60 px between the panels.
    s.text(520, 60, "People collaborating · paired machines", "h", "start")
    row = s.unit(520, 90, "You", "Your machine")
    s.unit(980, 90, "Colleague", "Colleague's machine")
    # Your Agent B to their Agent A: the right edge of one row to the left edge of the other.
    s.handshake(520 + 365, 980 + 35, row + 32)
    s.text(520, 545, "Each person chooses which agents to share.", "c", "start")
    return s


def generate_narrow(theme):
    """The same two scenes stacked, with larger type, for narrow columns."""
    s = Svg(THEMES[theme], 920, 1080, type_scale=1.15)
    s.text(40, 60, "One person · one machine", "h", "start")
    s.unit(40, 90, "You", "Your machine")
    s.text(40, 560, "People collaborating · paired machines", "h", "start")
    row = s.unit(40, 590, "You", "Your machine")
    s.unit(480, 590, "Colleague", "Their machine")  # the long heading overflows the narrow panel
    s.handshake(40 + 365, 480 + 35, row + 32)
    s.text(40, 1040, "Each person chooses which agents to share.", "c", "start")
    return s


def main(argv):
    out = Path(argv[1]) if len(argv) > 1 else Path(__file__).parent
    for theme in THEMES:
        generate(theme, "wide").save(out / ("collaboration-%s.svg" % theme))
        generate(theme, "narrow").save(out / ("collaboration-%s-narrow.svg" % theme))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
