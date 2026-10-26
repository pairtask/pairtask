package chat

import "strings"

// canvas is a grid of line joints and text. Lines are kept as the
// directions leaving each cell, so crossings and corners join by themselves;
// text and arrowheads sit on top.
type canvas struct {
	size  gridPos
	joint [][]uint8
	text  [][]string // "" empty; continuation cells of wide text hold "\x00"
}

// gridPos is a position on the canvas: a column and a row.
type gridPos struct{ x, y int }

func (p gridPos) right(n int) gridPos { return gridPos{p.x + n, p.y} }
func (p gridPos) down(n int) gridPos  { return gridPos{p.x, p.y + n} }

const (
	up uint8 = 1 << iota
	down
	left
	right
)

var joints = map[uint8]string{
	up: "│", down: "│", up | down: "│",
	left: "─", right: "─", left | right: "─",
	down | right: "┌", down | left: "┐", up | right: "└", up | left: "┘",
	up | down | right: "├", up | down | left: "┤",
	down | left | right: "┬", up | left | right: "┴",
	up | down | left | right: "┼",
}

// within is 1 ≤ n ≤ limit.
func within(n, limit int) bool { return n >= 1 && n <= limit }

// newCanvas allocates only within the rendering caps.
func newCanvas(size gridPos) (*canvas, error) {
	if !within(size.x, maxRenderedColumn) || !within(size.y, maxRenderedRows) {
		return nil, errUnsupported
	}
	c := &canvas{size: size, joint: make([][]uint8, size.y), text: make([][]string, size.y)}
	for y := range size.y {
		c.joint[y] = make([]uint8, size.x)
		c.text[y] = make([]string, size.x)
	}
	return c, nil
}

func (c *canvas) inside(p gridPos) bool {
	return within(p.x+1, c.size.x) && within(p.y+1, c.size.y)
}

func (c *canvas) join(p gridPos, d uint8) {
	if c.inside(p) {
		c.joint[p.y][p.x] |= d
	}
}

// line joins the cells from a to b, which share a row or a column.
func (c *canvas) line(a, b gridPos) {
	step, back, fwd := gridPos{1, 0}, left, right
	if a.x == b.x {
		step, back, fwd = gridPos{0, 1}, up, down
	}
	if b.x < a.x || b.y < a.y {
		a, b = b, a
	}
	for p := a; p != b; p = p.right(step.x).down(step.y) {
		c.join(p, fwd)
		c.join(p.right(step.x).down(step.y), back)
	}
}

// box draws a three-row box whose label starts two cells in.
func (c *canvas) box(at gridPos, l diagramLabel) {
	far := at.right(l.cells + 3).down(2)
	c.line(at, gridPos{far.x, at.y})
	c.line(gridPos{at.x, far.y}, far)
	c.line(at, gridPos{at.x, far.y})
	c.line(gridPos{far.x, at.y}, far)
	c.put(at.right(2).down(1), l)
}

// put writes a label; cells past the edge are dropped.
func (c *canvas) put(at gridPos, l diagramLabel) {
	if !c.inside(at) || !c.inside(at.right(l.cells-1)) {
		return
	}
	c.text[at.y][at.x] = l.text
	for i := 1; i < l.cells; i++ {
		c.text[at.y][at.x+i] = "\x00"
	}
}

func (c *canvas) mark(at gridPos, s string) { c.put(at, diagramLabel{text: s, cells: 1}) }

// vertical reports a line passing through p up or down.
func (c *canvas) vertical(p gridPos) bool { return c.inside(p) && c.joint[p.y][p.x]&(up|down) != 0 }

// rows renders the grid, trailing spaces trimmed.
func (c *canvas) rows() []string {
	out := make([]string, c.size.y)
	for y := range c.size.y {
		var b strings.Builder
		for x := range c.size.x {
			b.WriteString(c.glyph(gridPos{x, y}))
		}
		out[y] = strings.TrimRight(b.String(), " ")
	}
	return out
}

func (c *canvas) glyph(p gridPos) string {
	switch t := c.text[p.y][p.x]; t {
	case "\x00":
		return ""
	case "":
		if j, ok := joints[c.joint[p.y][p.x]]; ok {
			return j
		}
		return " "
	default:
		return t
	}
}
