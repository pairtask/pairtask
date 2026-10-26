package chat

import (
	"errors"
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// A strict Mermaid subset is drawn as text: sequence diagrams, and flowcharts
// whose graph is a forest. Anything outside the subset, or over a cap, is
// refused as a whole so the chat shows the source instead; nothing is
// dropped silently. Every cap is checked before layout, and layout never
// searches, so drawing stays linear in the diagram's size.

const (
	maxRenderSource   = 4 << 10
	maxRenderLines    = 200
	maxLabelCells     = 40
	maxNodes          = 30
	maxChildren       = 8
	maxParticipants   = 8
	maxMessages       = 60
	maxRenderedRows   = 150
	maxRenderedColumn = 200
)

var errUnsupported = errors.New("outside the drawable subset")

var (
	idPattern      = `[A-Za-z_][A-Za-z0-9_]*`
	nodeRe         = regexp.MustCompile(`^(` + idPattern + `)(?:\[([^\]]*)\]|\(([^)]*)\))?`)
	edgeRe         = regexp.MustCompile(`^\s*(-->|---)(?:\|([^|]*)\|)?\s*`)
	participantRe  = regexp.MustCompile(`^participant\s+(` + idPattern + `)(?:\s+as\s+(.+))?$`)
	messageRe      = regexp.MustCompile(`^(` + idPattern + `)\s*(-->>|->>|-->|->)\s*(` + idPattern + `)\s*:(.*)$`)
	forbiddenLabel = "[]()|\"<>{}`"
)

// statements are the source's meaningful lines: comments, blank lines and
// one trailing semicolon dropped; a directive refuses the whole diagram.
func (s diagramSource) statements() ([]string, error) {
	if len(s) > maxRenderSource || strings.Count(string(s), "\n") >= maxRenderLines {
		return nil, errUnsupported
	}
	var out []string
	for _, line := range strings.Split(string(s), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "%%{"):
			return nil, errUnsupported
		case line == "", strings.HasPrefix(line, "%%"):
			continue
		}
		out = append(out, strings.TrimSpace(strings.TrimSuffix(line, ";")))
	}
	if len(out) == 0 {
		return nil, errUnsupported
	}
	return out, nil
}

// diagramSource is a diagram's text between its fences.
type diagramSource string

// diagramLabel is a checked, escaped label and its width in cells.
type diagramLabel struct {
	text  string
	cells int
}

func parseLabel(raw string) (diagramLabel, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, forbiddenLabel) {
		return diagramLabel{}, errUnsupported
	}
	text := safeText(raw)
	cells := ansi.StringWidth(text)
	if cells > maxLabelCells {
		return diagramLabel{}, errUnsupported
	}
	return diagramLabel{text: text, cells: cells}, nil
}

// optionalLabel is an absent label, or a checked one.
func optionalLabel(raw string, present bool) (diagramLabel, error) {
	if !present {
		return diagramLabel{}, nil
	}
	return parseLabel(raw)
}
