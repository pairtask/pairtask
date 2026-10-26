package chat

// treeLayout places a forest as a tidy tree: each subtree owns a band
// across the levels (columns for TD, rows for LR), children sit side by side
// in their parent's band and a parent is centred over its children. One pass
// down and one back up, so layout is linear in the node count.
type treeLayout struct {
	g      *flowGraph
	band   []band
	center []int // the box centre across the levels
	depth  []int
	levels int
}

// band is the stretch across the levels a subtree owns.
type band struct{ start, span int }

// slot is where a subtree goes: its band's start and its level.
type slot struct{ pos, depth int }

func newTreeLayout(g *flowGraph) *treeLayout {
	n := len(g.nodes)
	t := &treeLayout{g: g, band: make([]band, n), center: make([]int, n), depth: make([]int, n)}
	at := slot{}
	for _, r := range g.roots() {
		t.measure(r)
		t.place(r, at)
		at.pos += t.band[r].span + t.rootGap()
	}
	return t
}

// siblingGap and rootGap separate bands: rows for LR, columns for TD.
func (t *treeLayout) siblingGap() int {
	if t.g.lr {
		return 1
	}
	return 2
}

func (t *treeLayout) rootGap() int {
	if t.g.lr {
		return 1
	}
	return 3
}

// size is a node's box extent across the levels.
func (t *treeLayout) size(n nodeID) int {
	if t.g.lr {
		return 3
	}
	return t.g.nodes[n].label.cells + 4
}

func (t *treeLayout) measure(n nodeID) {
	children := t.g.nodes[n].children
	for _, e := range children {
		t.measure(e.to)
	}
	span := max(t.size(n), t.bandsWidth(children))
	if in := t.g.nodes[n].in; !t.g.lr && in.cells > 0 {
		span = max(span, in.cells+2)
	}
	t.band[n].span = span
}

// bandsWidth is the children's bands side by side.
func (t *treeLayout) bandsWidth(children []flowEdge) int {
	total := 0
	for i, e := range children {
		if i > 0 {
			total += t.siblingGap()
		}
		total += t.band[e.to].span
	}
	return total
}

func (t *treeLayout) place(n nodeID, at slot) {
	t.band[n].start, t.depth[n] = at.pos, at.depth
	t.levels = max(t.levels, at.depth+1)
	children := t.g.nodes[n].children
	next := slot{pos: at.pos + (t.band[n].span-t.bandsWidth(children)+1)/2, depth: at.depth + 1}
	for _, e := range children {
		t.place(e.to, next)
		next.pos += t.band[e.to].span + t.siblingGap()
	}
	t.center[n] = t.centre(n)
}

// centre puts a box over the middle of its children, inside its band.
func (t *treeLayout) centre(n nodeID) int {
	b, size := t.band[n], t.size(n)
	mid := b.start + b.span/2
	if children := t.g.nodes[n].children; len(children) > 0 {
		mid = (t.center[children[0].to] + t.center[children[len(children)-1].to]) / 2
	}
	return min(max(mid, b.start+size/2), b.start+b.span-size+size/2)
}

// lead is where a node's box begins across the levels.
func (t *treeLayout) lead(n nodeID) int { return t.center[n] - t.size(n)/2 }

// extent is the total band width of the forest.
func (t *treeLayout) extent() int {
	total := 0
	for i, r := range t.g.roots() {
		if i > 0 {
			total += t.rootGap()
		}
		total += t.band[r].span
	}
	return total
}

// spread is the stretch from a parent's centre to its outermost children's.
func (t *treeLayout) spread(n nodeID) (lo, hi int) {
	children := t.g.nodes[n].children
	first, last := t.center[children[0].to], t.center[children[len(children)-1].to]
	return min(t.center[n], first), max(t.center[n], last)
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
		if node.parent >= 0 {
			info[d-1].addChild(node.in)
		}
	}
	return info
}

func (l *levelInfo) addChild(in diagramLabel) {
	l.parents = true
	if in.cells > 0 {
		l.labelled, l.label = true, max(l.label, in.cells)
	}
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
	for d, l := range info {
		tops[d] = height
		height += 3 + l.connector(2)
	}
	return tops, height
}

// connector is the room a level's edges take along the levels: base, plus
// room for labels.
func (l levelInfo) connector(base int) int {
	switch {
	case !l.parents:
		return 0
	case l.labelled:
		return base + 1
	}
	return base
}

func (t *treeLayout) drawTD() ([]string, error) {
	tops, height := tdRows(t.levelInfo())
	c, err := newCanvas(gridPos{t.extent(), height})
	if err != nil {
		return nil, err
	}
	for n, node := range t.g.nodes {
		id := nodeID(n)
		top := tops[t.depth[n]]
		c.box(gridPos{t.lead(id), top}, node.label)
		if len(node.children) > 0 {
			t.tdEdges(c, id, tdLink{bus: top + 3, target: tops[t.depth[n]+1]})
		}
	}
	return c.rows(), nil
}

// tdLink is where a parent's edges run in a top-down drawing: the bus row
// and the children's top row.
type tdLink struct{ bus, target int }

// tdEdges joins a parent to its children through a bus row under it.
func (t *treeLayout) tdEdges(c *canvas, n nodeID, l tdLink) {
	pc := t.center[n]
	c.line(gridPos{pc, l.bus - 1}, gridPos{pc, l.bus})
	lo, hi := t.spread(n)
	c.line(gridPos{lo, l.bus}, gridPos{hi, l.bus})
	for _, e := range t.g.nodes[n].children {
		cc := t.center[e.to]
		c.line(gridPos{cc, l.bus}, gridPos{cc, l.target - 1})
		if e.arrow {
			c.mark(gridPos{cc, l.target - 1}, "▼")
		}
		t.tdLabel(c, e.to, gridPos{cc, l.bus + 1})
	}
}

// tdLabel centres a child's edge label on its line, inside its band.
func (t *treeLayout) tdLabel(c *canvas, n nodeID, at gridPos) {
	in, b := t.g.nodes[n].in, t.band[n]
	if in.cells == 0 {
		return
	}
	at.x = min(max(at.x-in.cells/2, b.start), b.start+b.span-in.cells)
	c.put(at, in)
}

// lrColumns is each level's left column in a left-to-right drawing: the
// widest box, a stub, a bus column, room for the widest edge label, a line
// and an arrow.
func lrColumns(info []levelInfo) (lefts []int, width int) {
	lefts = make([]int, len(info))
	for d, l := range info {
		lefts[d] = width
		width += l.box + l.connector(4)
		if l.labelled {
			width += l.label
		}
	}
	return lefts, width
}

func (t *treeLayout) drawLR() ([]string, error) {
	info := t.levelInfo()
	lefts, width := lrColumns(info)
	c, err := newCanvas(gridPos{width, t.extent()})
	if err != nil {
		return nil, err
	}
	for n, node := range t.g.nodes {
		id, d := nodeID(n), t.depth[n]
		c.box(gridPos{lefts[d], t.lead(id)}, node.label)
		if len(node.children) > 0 {
			t.lrEdges(c, id, lrLink{edge: lefts[d] + node.label.cells + 3, bus: lefts[d] + info[d].box + 1, target: lefts[d+1]})
		}
	}
	return c.rows(), nil
}

// lrLink is where a parent's edges run in a left-to-right drawing: its box's
// right edge, the bus column and the children's left column.
type lrLink struct{ edge, bus, target int }

// lrEdges joins a parent's right edge to its children through a bus column.
func (t *treeLayout) lrEdges(c *canvas, n nodeID, l lrLink) {
	pr := t.center[n]
	c.line(gridPos{l.edge, pr}, gridPos{l.bus, pr})
	lo, hi := t.spread(n)
	c.line(gridPos{l.bus, lo}, gridPos{l.bus, hi})
	for _, e := range t.g.nodes[n].children {
		cr := t.center[e.to]
		c.line(gridPos{l.bus, cr}, gridPos{l.target - 1, cr})
		if e.arrow {
			c.mark(gridPos{l.target - 1, cr}, "►")
		}
		if in := t.g.nodes[e.to].in; in.cells > 0 {
			c.put(gridPos{l.bus + 2, cr}, in)
		}
	}
}
