package chat

import "strings"

// canvas is a grid of line joints and text. Lines are kept as the
// directions leaving each cell, so crossings and corners join by themselves;
// text and arrowheads sit on top.
type canvas struct {
	w, h  int
	joint [][]uint8
	text  [][]string // "" empty; continuation cells of wide text hold "\x00"
}

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

// newCanvas allocates only within the rendering caps.
func newCanvas(w, h int) (*canvas, error) {
	if w < 1 || h < 1 || w > maxRenderedColumn || h > maxRenderedRows {
		return nil, errUnsupported
	}
	c := &canvas{w: w, h: h, joint: make([][]uint8, h), text: make([][]string, h)}
	for y := range h {
		c.joint[y] = make([]uint8, w)
		c.text[y] = make([]string, w)
	}
	return c, nil
}

func (c *canvas) inside(x, y int) bool { return x >= 0 && y >= 0 && x < c.w && y < c.h }

func (c *canvas) join(x, y int, d uint8) {
	if c.inside(x, y) {
		c.joint[y][x] |= d
	}
}

// hline joins the cells from x0 to x1 on row y.
func (c *canvas) hline(x0, x1, y int) {
	x0, x1 = min(x0, x1), max(x0, x1)
	for x := x0; x <= x1; x++ {
		if x > x0 {
			c.join(x, y, left)
		}
		if x < x1 {
			c.join(x, y, right)
		}
	}
}

// vline joins the cells from y0 to y1 in column x.
func (c *canvas) vline(x, y0, y1 int) {
	y0, y1 = min(y0, y1), max(y0, y1)
	for y := y0; y <= y1; y++ {
		if y > y0 {
			c.join(x, y, up)
		}
		if y < y1 {
			c.join(x, y, down)
		}
	}
}

// box draws a three-row box whose label starts two cells in.
func (c *canvas) box(x, y int, l diagramLabel) {
	w := l.cells + 4
	c.hline(x, x+w-1, y)
	c.hline(x, x+w-1, y+2)
	c.vline(x, y, y+2)
	c.vline(x+w-1, y, y+2)
	c.put(x+2, y+1, l)
}

// put writes a label; cells past the edge are dropped.
func (c *canvas) put(x, y int, l diagramLabel) {
	if !c.inside(x, y) || !c.inside(x+l.cells-1, y) {
		return
	}
	c.text[y][x] = l.text
	for i := 1; i < l.cells; i++ {
		c.text[y][x+i] = "\x00"
	}
}

func (c *canvas) mark(x, y int, s string) { c.put(x, y, diagramLabel{text: s, cells: 1}) }

// rows renders the grid, trailing spaces trimmed.
func (c *canvas) rows() []string {
	out := make([]string, c.h)
	for y := range c.h {
		var b strings.Builder
		for x := range c.w {
			b.WriteString(c.cell(x, y))
		}
		out[y] = strings.TrimRight(b.String(), " ")
	}
	return out
}

func (c *canvas) cell(x, y int) string {
	switch t := c.text[y][x]; t {
	case "\x00":
		return ""
	case "":
		if j, ok := joints[c.joint[y][x]]; ok {
			return j
		}
		return " "
	default:
		return t
	}
}
