package chat

import "strings"

// flowGraph is a parsed flowchart: nodes in order of first appearance, each
// with its outgoing edges in source order.
type flowGraph struct {
	lr    bool
	nodes []flowNode
	index map[string]nodeID
	edges map[flowEdgeKey]bool
}

// nodeID is a node's place in flowGraph.nodes.
type nodeID int

type flowNode struct {
	id       string
	label    diagramLabel
	declared bool   // the label was given, not implied by the ID
	parent   nodeID // -1 for a root
	in       diagramLabel
	children []flowEdge
}

type flowEdge struct {
	to    nodeID
	arrow bool
}

type flowEdgeKey struct {
	from, to nodeID
	label    string
}

// flowHeader reads "flowchart LR|TD" or "graph LR|TD".
func flowHeader(line string) (lr, ok bool) {
	f := strings.Fields(line)
	if len(f) != 2 || !flowKinds[f[0]] {
		return false, false
	}
	lr, ok = flowDirections[f[1]]
	return lr, ok
}

var (
	flowKinds      = map[string]bool{"flowchart": true, "graph": true}
	flowDirections = map[string]bool{"LR": true, "TD": false} // true for LR
)

func parseFlow(lr bool, statements []string) (*flowGraph, error) {
	g := &flowGraph{lr: lr, index: map[string]nodeID{}, edges: map[flowEdgeKey]bool{}}
	for _, st := range statements {
		if err := g.statement(st); err != nil {
			return nil, err
		}
	}
	return g, g.checkForest()
}

// statement is a node, or a chain of nodes joined by edges.
func (g *flowGraph) statement(st string) error {
	from, rest, err := g.node(st)
	if err != nil {
		return err
	}
	for rest != "" {
		m := edgeRe.FindStringSubmatchIndex(rest)
		if m == nil {
			return errUnsupported
		}
		label, err := optionalLabel(sub(rest, m, 2))
		if err != nil {
			return err
		}
		arrow := rest[m[2]:m[3]] == "-->"
		to, next, err := g.node(rest[m[1]:])
		if err != nil {
			return err
		}
		if err := g.edge(from, to, flowEdge{to: to, arrow: arrow}, label); err != nil {
			return err
		}
		from, rest = to, next
	}
	return nil
}

// node reads one node reference or declaration and returns the rest.
func (g *flowGraph) node(s string) (nodeID, string, error) {
	m := nodeRe.FindStringSubmatchIndex(s)
	if m == nil {
		return 0, "", errUnsupported
	}
	raw, present := sub(s, m, 2)
	if !present {
		raw, present = sub(s, m, 3)
	}
	label, err := optionalLabel(raw, present)
	if err != nil {
		return 0, "", err
	}
	n, err := g.declare(s[m[2]:m[3]], label, present)
	return n, strings.TrimSpace(s[m[1]:]), err
}

// declare finds or adds a node; a second, different label is a conflict.
func (g *flowGraph) declare(id string, label diagramLabel, declared bool) (nodeID, error) {
	n, ok := g.index[id]
	if !ok {
		if len(g.nodes) == maxNodes {
			return 0, errUnsupported
		}
		n = nodeID(len(g.nodes))
		g.index[id] = n
		g.nodes = append(g.nodes, flowNode{id: id, label: diagramLabel{text: id, cells: len(id)}, parent: -1})
	}
	node := &g.nodes[n]
	switch {
	case !declared:
	case node.declared && node.label != label:
		return 0, errUnsupported
	default:
		node.label, node.declared = label, true
	}
	if node.label.cells > maxLabelCells {
		return 0, errUnsupported
	}
	return n, nil
}

// edge links from to its child; a node has one parent, and an edge is
// never repeated.
func (g *flowGraph) edge(from, to nodeID, e flowEdge, label diagramLabel) error {
	key := flowEdgeKey{from: from, to: to, label: label.text}
	if !g.canLink(key) {
		return errUnsupported
	}
	g.edges[key] = true
	g.nodes[to].parent, g.nodes[to].in = from, label
	g.nodes[from].children = append(g.nodes[from].children, e)
	return nil
}

// canLink allows a new edge to a node without a parent, under the
// children cap.
func (g *flowGraph) canLink(key flowEdgeKey) bool {
	switch {
	case g.edges[key], key.from == key.to:
		return false
	case g.nodes[key.to].parent >= 0:
		return false
	}
	return len(g.nodes[key.from].children) < maxChildren
}

// checkForest refuses a cycle: every node must be reachable from a root.
func (g *flowGraph) checkForest() error {
	seen := 0
	for _, r := range g.roots() {
		seen += g.count(r)
	}
	if seen != len(g.nodes) {
		return errUnsupported
	}
	return nil
}

func (g *flowGraph) roots() []nodeID {
	var out []nodeID
	for i, n := range g.nodes {
		if n.parent < 0 {
			out = append(out, nodeID(i))
		}
	}
	return out
}

// count is the size of n's subtree; one parent per node bounds it by the
// node count.
func (g *flowGraph) count(n nodeID) int {
	total := 1
	for _, e := range g.nodes[n].children {
		total += g.count(e.to)
	}
	return total
}

// sub is submatch i of a FindStringSubmatchIndex result and whether it
// matched at all.
func sub(s string, m []int, i int) (string, bool) {
	if m[2*i] < 0 {
		return "", false
	}
	return s[m[2*i]:m[2*i+1]], true
}
