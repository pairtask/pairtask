package chat

// treeLayout places a forest as a tidy tree: each subtree owns a band
// across the levels (columns for TD, rows for LR), children sit side by side
// in their parent's band and a parent is centred over its children. One pass
// down and one back up, so layout is linear in the node count.
type treeLayout struct {
	g      *flowGraph
	span   []int // the subtree's band width
	start  []int // where the band begins
	center []int // the box centre across the levels
	depth  []int
	levels int
}

const (
	siblingGap = 2 // between sibling bands, TD
	rootGap    = 3 // between trees
)

func newTreeLayout(g *flowGraph) *treeLayout {
	n := len(g.nodes)
	t := &treeLayout{g: g, span: make([]int, n), start: make([]int, n), center: make([]int, n), depth: make([]int, n)}
	pos := 0
	for _, r := range g.roots() {
		t.measure(r)
		t.place(r, pos, 0)
		pos += t.span[r] + t.gap(rootGap)
	}
	return t
}

// gap is the spacing between bands along the band axis: rows for LR.
func (t *treeLayout) gap(td int) int {
	if t.g.lr {
		return 1
	}
	return td
}

// size is a node's box extent across the levels.
func (t *treeLayout) size(n int) int {
	if t.g.lr {
		return 3
	}
	return t.g.nodes[n].label.cells + 4
}

func (t *treeLayout) measure(n int) int {
	node := t.g.nodes[n]
	inner := t.childrenSpan(n)
	span := max(t.size(n), inner)
	if !t.g.lr && node.in.cells > 0 {
		span = max(span, node.in.cells+2)
	}
	t.span[n] = span
	return span
}

// childrenSpan measures the children and returns their bands' total width.
func (t *treeLayout) childrenSpan(n int) int {
	children := t.g.nodes[n].children
	total := 0
	for _, e := range children {
		total += t.measure(e.to)
	}
	if len(children) > 1 {
		total += (len(children) - 1) * t.gap(siblingGap)
	}
	return total
}

func (t *treeLayout) place(n, at, depth int) {
	t.start[n], t.depth[n] = at, depth
	t.levels = max(t.levels, depth+1)
	children := t.g.nodes[n].children
	inner := t.span[n] - t.innerSpan(n)
	pos := at + (inner+1)/2
	for _, e := range children {
		t.place(e.to, pos, depth+1)
		pos += t.span[e.to] + t.gap(siblingGap)
	}
	mid := at + t.span[n]/2
	if len(children) > 0 {
		mid = (t.center[children[0].to] + t.center[children[len(children)-1].to]) / 2
	}
	lo := at + t.size(n)/2
	hi := at + t.span[n] - t.size(n) + t.size(n)/2
	t.center[n] = min(max(mid, lo), hi)
}

func (t *treeLayout) innerSpan(n int) int {
	children := t.g.nodes[n].children
	total := 0
	for _, e := range children {
		total += t.span[e.to]
	}
	if len(children) > 1 {
		total += (len(children) - 1) * t.gap(siblingGap)
	}
	return total
}

// lead is where a node's box begins across the levels.
func (t *treeLayout) lead(n int) int { return t.center[n] - t.size(n)/2 }

// extent is the total band width of the forest.
func (t *treeLayout) extent() int {
	roots := t.g.roots()
	total := (len(roots) - 1) * t.gap(rootGap)
	for _, r := range roots {
		total += t.span[r]
	}
	return total
}

// levelInfo is, per level, whether its nodes have children and whether any
// edge leaving it carries a label, and the widest such label and box.
type levelInfo struct {
	parents  bool
	labelled bool
	label    int
	box      int
}

func (t *treeLayout) levelInfo() []levelInfo {
	info := make([]levelInfo, t.levels)
	for n, node := range t.g.nodes {
		d := t.depth[n]
		info[d].box = max(info[d].box, node.label.cells+4)
		if node.parent < 0 {
			continue
		}
		p := &info[d-1]
		p.parents = true
		if node.in.cells > 0 {
			p.labelled, p.label = true, max(p.label, node.in.cells)
		}
	}
	return info
}

// drawTree renders the forest top-down or left-to-right.
func drawTree(g *flowGraph) ([]string, error) {
	t := newTreeLayout(g)
	if g.lr {
		return t.drawLR()
	}
	return t.drawTD()
}

// tdRows is each level's top row in a top-down drawing: a box, then a bus
// row, an optional label row and an arrow row under every level with
// children.
func tdRows(info []levelInfo) (tops []int, height int) {
	tops = make([]int, len(info))
	y := 0
	for d, l := range info {
		tops[d] = y
		y += 3
		if l.parents {
			y += 2
			if l.labelled {
				y++
			}
		}
	}
	return tops, y
}

func (t *treeLayout) drawTD() ([]string, error) {
	info := t.levelInfo()
	tops, height := tdRows(info)
	c, err := newCanvas(t.extent(), height)
	if err != nil {
		return nil, err
	}
	for n, node := range t.g.nodes {
		top := tops[t.depth[n]]
		c.box(t.lead(n), top, node.label)
		if len(node.children) > 0 {
			t.tdEdges(c, n, top+3, tops[t.depth[n]+1])
		}
	}
	return c.rows(), nil
}

// tdEdges joins a parent to its children through a bus row under it.
func (t *treeLayout) tdEdges(c *canvas, n, bus, childTop int) {
	children := t.g.nodes[n].children
	pc := t.center[n]
	c.vline(pc, bus-1, bus)
	first, last := t.center[children[0].to], t.center[children[len(children)-1].to]
	c.hline(min(pc, first), max(pc, last), bus)
	for _, e := range children {
		cc := t.center[e.to]
		c.vline(cc, bus, childTop-1)
		if e.arrow {
			c.mark(cc, childTop-1, "▼")
		}
		if in := t.g.nodes[e.to].in; in.cells > 0 {
			x := min(max(cc-in.cells/2, t.start[e.to]), t.start[e.to]+t.span[e.to]-in.cells)
			c.put(x, bus+1, in)
		}
	}
}

// lrColumns is each level's left column in a left-to-right drawing: the
// widest box, a stub, a bus column, room for the widest edge label, a line
// and an arrow.
func lrColumns(info []levelInfo) (lefts []int, width int) {
	lefts = make([]int, len(info))
	x := 0
	for d, l := range info {
		lefts[d] = x
		x += l.box
		if l.parents {
			x += 4
			if l.labelled {
				x += l.label + 1
			}
		}
	}
	return lefts, x
}

func (t *treeLayout) drawLR() ([]string, error) {
	info := t.levelInfo()
	lefts, width := lrColumns(info)
	c, err := newCanvas(width, t.extent())
	if err != nil {
		return nil, err
	}
	for n, node := range t.g.nodes {
		d := t.depth[n]
		c.box(lefts[d], t.lead(n), node.label)
		if len(node.children) > 0 {
			t.lrEdges(c, n, lefts[d]+node.label.cells+3, lefts[d]+info[d].box+1, lefts[d+1])
		}
	}
	return c.rows(), nil
}

// lrEdges joins a parent's right edge to its children through a bus column.
func (t *treeLayout) lrEdges(c *canvas, n, edge, bus, childLeft int) {
	children := t.g.nodes[n].children
	pr := t.center[n]
	c.hline(edge, bus, pr)
	first, last := t.center[children[0].to], t.center[children[len(children)-1].to]
	c.vline(bus, min(pr, first), max(pr, last))
	for _, e := range children {
		cr := t.center[e.to]
		c.hline(bus, childLeft-1, cr)
		if e.arrow {
			c.mark(childLeft-1, cr, "►")
		}
		if in := t.g.nodes[e.to].in; in.cells > 0 {
			c.put(bus+2, cr, in)
		}
	}
}
