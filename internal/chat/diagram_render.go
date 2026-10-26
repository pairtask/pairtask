package chat

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// drawing is a diagram rendered as text rows, and its widest row.
type drawing struct {
	rows  []string
	cells int
}

// renderMermaid draws a Mermaid diagram from the subset, or reports false so
// its source is shown instead. A panic is a refusal too: the renderer is
// bounded by its caps, and recovering is only the last line of defence.
func renderMermaid(src diagramSource) (out drawing, ok bool) {
	defer func() {
		if recover() != nil {
			out, ok = drawing{}, false
		}
	}()
	rows, err := drawMermaid(src)
	if err != nil {
		return drawing{}, false
	}
	cells := 0
	for _, r := range rows {
		cells = max(cells, ansi.StringWidth(r))
	}
	return drawing{rows: rows, cells: cells}, true
}

func drawMermaid(src diagramSource) ([]string, error) {
	statements, err := src.statements()
	if err != nil {
		return nil, err
	}
	head, body := statements[0], statements[1:]
	if head == "sequenceDiagram" {
		s, err := parseSequence(body)
		if err != nil {
			return nil, err
		}
		return drawSequence(s)
	}
	lr, ok := flowHeader(head)
	if !ok {
		return nil, errUnsupported
	}
	g, err := parseFlow(lr, body)
	if err != nil {
		return nil, err
	}
	if len(g.nodes) == 0 {
		return nil, errUnsupported
	}
	return drawTree(g)
}

// diagramRenders holds one entry's drawings by diagram, for the body hash
// they were drawn from; a message has at most maxDiagrams, and the cache
// goes with its entry.
type diagramRenders struct {
	hash   string
	byOpen map[int]renderResult
}

type renderResult struct {
	drawing drawing
	ok      bool
}

// drawingsFor is e's cache, renewed when its body changes.
func (e *entry) drawingsFor() *diagramRenders {
	if e.drawings == nil || e.drawings.hash != e.hash {
		e.drawings = &diagramRenders{hash: e.hash, byOpen: map[int]renderResult{}}
	}
	return e.drawings
}

// renderable is the drawing of a closed mermaid diagram, if it has one,
// drawn at most once per cache; a nil cache draws directly.
func (r *diagramRenders) renderable(d diagram) (drawing, bool) {
	if !d.drawable() {
		return drawing{}, false
	}
	if r == nil {
		return renderMermaid(diagramSource(d.source))
	}
	if hit, ok := r.byOpen[d.open]; ok {
		return hit.drawing, hit.ok
	}
	drawn, ok := renderMermaid(diagramSource(d.source))
	r.byOpen[d.open] = renderResult{drawing: drawn, ok: ok}
	return drawn, ok
}

// drawable is a non-empty mermaid diagram within the source cap; the rest
// of the subset is checked while parsing.
func (d diagram) drawable() bool {
	if d.lang != "mermaid" {
		return false
	}
	return len(d.source) <= maxRenderSource && strings.TrimSpace(d.source) != ""
}
