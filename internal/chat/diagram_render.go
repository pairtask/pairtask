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

// diagramRenders caches drawings by source, so layout and View never draw
// the same diagram twice; it holds a bounded number, dropping the oldest.
type diagramRenders struct {
	byKey map[diagramSource]renderResult
	order []diagramSource
}

type renderResult struct {
	drawing drawing
	ok      bool
}

const maxCachedRenders = 64

// render draws src through the cache; a nil cache draws directly.
func (r *diagramRenders) render(src diagramSource) (drawing, bool) {
	if r == nil {
		return renderMermaid(src)
	}
	if hit, ok := r.byKey[src]; ok {
		return hit.drawing, hit.ok
	}
	d, ok := renderMermaid(src)
	if r.byKey == nil {
		r.byKey = map[diagramSource]renderResult{}
	}
	if len(r.order) == maxCachedRenders {
		delete(r.byKey, r.order[0])
		r.order = r.order[1:]
	}
	r.byKey[src] = renderResult{drawing: d, ok: ok}
	r.order = append(r.order, src)
	return d, ok
}

// renderable is the drawing of a closed mermaid diagram, if it has one.
func (r *diagramRenders) renderable(d diagram) (drawing, bool) {
	if d.lang != "mermaid" || len(d.source) > maxRenderSource || strings.TrimSpace(d.source) == "" {
		return drawing{}, false
	}
	return r.render(diagramSource(d.source))
}
