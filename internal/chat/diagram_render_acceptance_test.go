package chat

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

type diagramRenderCase struct {
	name, source string
	labels       []string
}

type diagramRenderView struct {
	lines []renderLine
	next  int
	width int
}

func (c diagramRenderCase) format(width int) diagramRenderView {
	b := lineBuilder{id: "render-acceptance", seq: 7, src: 17}
	b.formatBody("```mermaid\n"+c.source+"\n```\nafter", width)
	return diagramRenderView{lines: b.lines, next: b.src, width: width}
}

func (v diagramRenderView) text() string {
	var rows []string
	for _, row := range v.lines {
		rows = append(rows, ansi.Strip(row.text))
	}
	return strings.Join(rows, "\n")
}

func (v diagramRenderView) rendered() bool {
	for _, row := range v.lines {
		if row.src == 17 && strings.Contains(ansi.Strip(row.text), " · rendered") {
			return true
		}
	}
	return false
}

func (c diagramRenderCase) wantRendered(t *testing.T, v diagramRenderView) {
	t.Helper()
	if !v.rendered() {
		t.Fatalf("expected rendered diagram, got %q", v.text())
	}
	for _, label := range c.labels {
		if !strings.Contains(v.text(), label) {
			t.Errorf("rendered diagram lost label %q", label)
		}
	}
	for _, header := range []string{"sequenceDiagram", "flowchart ", "graph "} {
		if strings.Contains(v.text(), header) {
			t.Errorf("rendered diagram still displays source header %q", header)
		}
	}
	if len(v.lines)-2 > 150 {
		t.Errorf("rendered output exceeds 150 rows: %d", len(v.lines)-2)
	}
	v.bounds(t)
}

func (v diagramRenderView) bounds(t *testing.T) {
	t.Helper()
	for _, row := range v.lines {
		if row.src == v.next-1 {
			continue
		} // unrelated prose after the closing fence
		if ansi.StringWidth(row.text) > v.width {
			t.Errorf("diagram exceeds width %d: %q", v.width, row.text)
		}
	}
}

func (c diagramRenderCase) wantSource(t *testing.T, v diagramRenderView) {
	t.Helper()
	if v.rendered() {
		t.Fatalf("unsupported or oversized diagram rendered: %q", v.text())
	}
	lines := strings.Split(c.source, "\n")
	got := v.sourceLines(t, len(lines))
	for i, line := range lines {
		if got[i] != safeText(line) {
			t.Errorf("fallback source line %d = %q, want %q", i, got[i], safeText(line))
		}
	}
	v.bounds(t)
}

func (v diagramRenderView) sourceLines(t *testing.T, count int) []string {
	t.Helper()
	got := make([]string, count)
	for _, row := range v.lines {
		i := row.src - 18
		if i < 0 || i >= len(got) {
			continue
		}
		got[i] += v.codeText(t, row.text)
	}
	return got
}

func (v diagramRenderView) codeText(t *testing.T, text string) string {
	t.Helper()
	in := min(2, max(0, v.width-1))
	plain := ansi.Strip(text)
	if len(plain) < in {
		t.Fatalf("missing code indent: %q", plain)
	}
	return plain[in:]
}

func TestDiagramRenderSupportedSubset(t *testing.T) {
	cases := []diagramRenderCase{
		{"flow LR", "flowchart LR\nA[Start] --> B(Ready) --> C[Done]", []string{"Start", "Ready", "Done"}},
		{"flow TD branching", "graph TD\nA[Root] -->|yes| B[Left]\nA --> C[Right]", []string{"Root", "Left", "Right", "yes"}},
		{"undirected edge", "flowchart TD\nA[First] --- B[Second]", []string{"First", "Second"}},
		{"forest", "graph LR\nA[One]\nB[Two]", []string{"One", "Two"}},
		{"sequence", "sequenceDiagram\nparticipant A as Alice\nparticipant B as Bob\nA->>B: hello\nB-->>A: reply\nA->B: ask\nB-->A: answer", []string{"Alice", "Bob", "hello", "reply", "ask", "answer"}},
		{"self message", "sequenceDiagram\nA->>A: thinking", []string{"A", "thinking"}},
		{"comments", "\n%% overview\nflowchart TD\n\nA[Start] --> B[End]\n%% finish", []string{"Start", "End"}},
		{"wide labels", "flowchart TD\nA[入口] --> B[出口]", []string{"入口", "出口"}},
		{"header whitespace", "flowchart\tTD\nA[Start] --> B[End]", []string{"Start", "End"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := c.format(220)
			c.wantRendered(t, v)
		})
	}
}

func TestDiagramRenderUnsupportedSourceIsNeverLost(t *testing.T) {
	cases := []diagramRenderCase{
		{name: "missing header", source: "A --> B"},
		{name: "unknown kind", source: "pie\nA: 1"},
		{name: "reverse direction", source: "flowchart RL\nA --> B"},
		{name: "cycle", source: "flowchart TD\nA --> B\nB --> A\nC --> A\nA --> C"},
		{name: "merge", source: "flowchart TD\nA --> C\nB --> C"},
		{name: "self edge", source: "flowchart TD\nA --> A"},
		{name: "parallel group", source: "flowchart TD\nA & B --> C"},
		{name: "unknown line", source: "flowchart TD\nA --> B\nunsupported? lost"},
		{name: "unfinished label", source: "flowchart TD\nA[unfinished --> B"},
		{name: "unknown arrow", source: "flowchart TD\nA ==> B"},
		{name: "shape", source: "flowchart TD\nA{Decision} --> B"},
		{name: "subgraph", source: "flowchart TD\nsubgraph cluster\nA --> B\nend"},
		{name: "style", source: "flowchart TD\nA --> B\nstyle A fill:red"},
		{name: "remote click", source: "flowchart TD\nA --> B\nclick A \"https://example.invalid/\""},
		{name: "init directive", source: "%%{init: {'theme':'dark'}}%%\nflowchart TD\nA --> B"},
		{name: "external include", source: "flowchart TD\nA --> B\n!include /etc/passwd"},
		{name: "note panic regression", source: "sequenceDiagram\nA->>B: hi\nNote over ,: empty actors"},
		{name: "loop", source: "sequenceDiagram\nloop until ready\nA->>B: wait\nend"},
		{name: "unknown message", source: "sequenceDiagram\nA-xB: gone"},
		{name: "duplicate edge", source: "flowchart TD\nA --> B\nA --> B"},
		{name: "conflicting label", source: "flowchart TD\nA[One] --> B\nA[Two]"},
		{name: "bad identifier", source: "flowchart TD\n1A --> B"},
		{name: "empty label", source: "flowchart TD\nA[ ] --> B"},
		{name: "HTML label", source: "flowchart TD\nA[one<br>two] --> B"},
		{name: "quoted label", source: "flowchart TD\nA[\"quoted\"] --> B"},
		{name: "empty message", source: "sequenceDiagram\nA->>B: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.wantSource(t, c.format(220)) })
	}
}

func TestDiagramRenderInputLimitsFallbackBeforeLayout(t *testing.T) {
	cases := []diagramRenderCase{
		{name: "source bytes", source: "flowchart TD\nA --> B\n%%" + strings.Repeat("x", 4096)},
		{name: "source lines", source: "flowchart TD\nA --> B" + strings.Repeat("\n", 199)},
		{name: "node label cells", source: "flowchart TD\nA[" + strings.Repeat("x", 41) + "] --> B"},
		{name: "wide node label cells", source: "flowchart TD\nA[" + strings.Repeat("界", 21) + "] --> B"},
		{name: "edge label cells", source: "flowchart TD\nA -->|" + strings.Repeat("x", 41) + "| B"},
		{name: "participant label cells", source: "sequenceDiagram\nparticipant A as " + strings.Repeat("x", 41) + "\nA->>B: hi"},
		{name: "message label cells", source: "sequenceDiagram\nA->>B: " + strings.Repeat("x", 41)},
		{name: "messages", source: "sequenceDiagram" + strings.Repeat("\nA->>B: hi", 61)},
		{name: "nodes", source: diagramRenderNodes(31)},
		{name: "children", source: diagramRenderChildren(9)},
		{name: "participants", source: diagramRenderParticipants(9)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.wantSource(t, c.format(220)) })
	}
}

func diagramRenderNodes(n int) string {
	var b strings.Builder
	b.WriteString("flowchart TD")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "\nN%d", i)
	}
	return b.String()
}

func diagramRenderChildren(n int) string {
	var b strings.Builder
	b.WriteString("flowchart TD")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "\nRoot --> N%d", i)
	}
	return b.String()
}

func diagramRenderParticipants(n int) string {
	var b strings.Builder
	b.WriteString("sequenceDiagram")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "\nparticipant P%d", i)
	}
	b.WriteString("\nP0->>P1: hello")
	return b.String()
}

func TestDiagramRenderInclusiveSourceAndLabelLimits(t *testing.T) {
	base := "flowchart TD\nA --> B\n%%"
	cases := []diagramRenderCase{
		{name: "exact source bytes", source: base + strings.Repeat("x", 4096-len(base))},
		{name: "exact source lines", source: "flowchart TD\nA --> B" + strings.Repeat("\n", 198)},
		{name: "exact label cells", source: "flowchart TD\nA[" + strings.Repeat("界", 20) + "] --> B"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.wantRendered(t, c.format(220)) })
	}
}

func TestDiagramRenderWidthFallbackPreservesWholeSource(t *testing.T) {
	c := diagramRenderCase{source: "flowchart LR\nA[Starting] --> B[Finished]", labels: []string{"Starting", "Finished"}}
	for _, width := range []int{1, 2, 10} {
		t.Run(strconv.Itoa(width), func(t *testing.T) { c.wantSource(t, c.format(width)) })
	}
	c.wantRendered(t, c.format(100))
}

func TestDiagramRenderSourceSlotsAndRowAnchors(t *testing.T) {
	c := diagramRenderCase{source: "flowchart TD\nA --> B --> C"}
	v := c.format(100)
	c.wantRendered(t, v)
	sourceCount := len(strings.Split(c.source, "\n"))
	if v.next != 17+sourceCount+3 {
		t.Fatalf("source accounting shifted: %d", v.next)
	}
	r := 0
	for _, row := range v.lines {
		if row.src == 17 || row.src == v.next-1 {
			continue
		}
		wantSrc, wantCol := 18+min(r, sourceCount-1), max(0, r-(sourceCount-1))
		if row.src != wantSrc || row.col != wantCol {
			t.Errorf("render row %d anchor=(%d,%d), want=(%d,%d)", r, row.src, row.col, wantSrc, wantCol)
		}
		r++
	}
	if r <= sourceCount {
		t.Errorf("fixture must render more rows than source: %d", r)
	}
}

func TestDiagramRenderEscapesTerminalControls(t *testing.T) {
	c := diagramRenderCase{source: "flowchart TD\nA[before\x1b[2J\u202eafter] --> B"}
	v := c.format(220)
	for _, control := range []string{"\x1b[2J", "\u202e"} {
		if strings.Contains(v.text(), control) {
			t.Errorf("terminal control reached output: %q", control)
		}
	}
	for _, escaped := range []string{`\x1b`, `\u202e`} {
		if !strings.Contains(v.text(), escaped) {
			t.Errorf("escaped control lost: %q", escaped)
		}
	}
}

func TestDiagramRenderHiddenSourceAnchorStaysInItsBlock(t *testing.T) {
	source := "flowchart TD\nA --> B" + strings.Repeat("\n%% comment", 60)
	block := "```mermaid\n" + source + "\n```"
	b := lineBuilder{id: "two-diagrams", seq: 7, src: 17}
	b.formatBody(block+"\nbetween\n"+block+"\nafter", 100)
	sourceCount := len(strings.Split(source, "\n"))
	for _, open := range []int{17, 17 + sourceCount + 3} {
		block := diagramAnchorBlock{lines: b.lines, open: open, slots: sourceCount}
		last := block.lastDrawnRow(t)
		m := Model{view: viewport{anchorID: b.id, anchorSrc: open + sourceCount, anchorCol: 0}}
		if got := m.anchorIndex(b.lines); got != last {
			t.Errorf("hidden slot in block %d anchors row %d, want last rendered row %d", open, got, last)
		}
	}
}

type diagramAnchorBlock struct {
	lines       []renderLine
	open, slots int
}

func (b diagramAnchorBlock) lastDrawnRow(t *testing.T) int {
	t.Helper()
	last, rendered := -1, false
	for i, row := range b.lines {
		if row.src == b.open && strings.Contains(ansi.Strip(row.text), " · rendered") {
			rendered = true
		}
		if row.src > b.open && row.src < b.open+b.slots+1 {
			last = i
		}
	}
	if !rendered || last < 0 {
		t.Fatalf("fixture did not render block at %d", b.open)
	}
	return last
}

func TestDiagramRenderBlankCommentsAndTrailingSemicolons(t *testing.T) {
	c := diagramRenderCase{source: "flowchart TD;\nA[Start];\nA[Start];\nA --> B[Finish];", labels: []string{"Start", "Finish"}}
	c.wantRendered(t, c.format(100))
}

func TestDiagramRenderOutputLimitsFallback(t *testing.T) {
	cases := []diagramRenderCase{
		{name: "canvas width", source: "flowchart LR\nA[" + strings.Repeat("a", 40) + "] --> B[" + strings.Repeat("b", 40) + "] --> C[" + strings.Repeat("c", 40) + "] --> D[" + strings.Repeat("d", 40) + "] --> E[" + strings.Repeat("e", 40) + "]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.wantSource(t, c.format(300)) })
	}
}

func TestDiagramRenderEntryCacheTracksBodyAndBlock(t *testing.T) {
	m := Model{width: 100}
	e := &entry{msg: protocol.Message{MessageID: "same-message", Seq: 7, From: "author", To: "operator"}, direction: "received"}
	for _, label := range []string{"FirstBody", "ChangedBody", "FirstBody"} {
		body := "```mermaid\nflowchart TD\nA[" + label + "] --> B\n```\n```mermaid\nsequenceDiagram\nX->>Y: second block\n```"
		var err error
		e.msg.Payload, err = json.Marshal(map[string]string{"text": body})
		if err != nil {
			t.Fatal(err)
		}
		e.hash = bodyHash(e.msg.Payload)
		v := diagramRenderView{lines: m.entryLines(e)}
		if strings.Count(v.text(), " · rendered") != 2 {
			t.Fatalf("two blocks did not render: %s", v.text())
		}
		for _, want := range []string{label, "second block"} {
			if !strings.Contains(v.text(), want) {
				t.Errorf("cache returned stale/wrong block, missing %q", want)
			}
		}
		other := "FirstBody"
		if label == other {
			other = "ChangedBody"
		}
		if strings.Contains(v.text(), other) {
			t.Errorf("cache retained old body %q", other)
		}
	}
}

func TestDiagramRenderPreservesFlowDirectionAndEdges(t *testing.T) {
	c := diagramRenderCase{source: "flowchart LR\nA[Sender] --> B[Receiver]"}
	v := c.format(100)
	c.wantRendered(t, v)
	fromRow, fromCol := v.labelPosition(t, "Sender")
	toRow, toCol := v.labelPosition(t, "Receiver")
	if fromRow != toRow || fromCol >= toCol {
		t.Errorf("LR flow positions reversed/misaligned: (%d,%d) to (%d,%d)", fromRow, fromCol, toRow, toCol)
	}
	if strings.Count(v.text(), "►") != 1 {
		t.Error("directed edge must have one right arrowhead")
	}
	c.source = "flowchart LR\nA[Sender] --- B[Receiver]"
	v = c.format(100)
	c.wantRendered(t, v)
	if strings.ContainsAny(v.text(), "►◄▼▲") {
		t.Error("undirected edge acquired an arrowhead")
	}
}

func TestDiagramRenderBranchChildrenStayBelowParent(t *testing.T) {
	c := diagramRenderCase{source: "flowchart TD\nRoot --> Left\nRoot --> Right"}
	v := c.format(100)
	c.wantRendered(t, v)
	rootY, rootX := v.labelPosition(t, "Root")
	leftY, leftX := v.labelPosition(t, "Left")
	rightY, rightX := v.labelPosition(t, "Right")
	if rootY >= leftY || leftY != rightY {
		t.Errorf("tree levels lost: root row %d, child rows %d and %d", rootY, leftY, rightY)
	}
	if leftX >= rootX || rootX >= rightX {
		t.Errorf("parent no longer between children: root column %d, child columns %d and %d", rootX, leftX, rightX)
	}
	if strings.Count(v.text(), "▼") != 2 {
		t.Error("branch must retain both directed edges")
	}
}

func (v diagramRenderView) labelPosition(t *testing.T, label string) (int, int) {
	t.Helper()
	for y, row := range v.lines {
		plain := ansi.Strip(row.text)
		if x := strings.Index(plain, label); x >= 0 {
			return y, ansi.StringWidth(plain[:x])
		}
	}
	t.Fatalf("label %q missing from %s", label, v.text())
	return -1, -1
}

func TestDiagramRenderSequenceKeepsMessageOrderAndDirection(t *testing.T) {
	c := diagramRenderCase{source: "sequenceDiagram\nA->>B: request\nB-->>A: reply"}
	v := c.format(100)
	c.wantRendered(t, v)
	requestY, _ := v.labelPosition(t, "request")
	replyY, _ := v.labelPosition(t, "reply")
	if requestY >= replyY {
		t.Error("sequence messages changed order")
	}
	for _, mark := range []string{"►", "◄", "┄"} {
		if !strings.Contains(v.text(), mark) {
			t.Errorf("sequence lost message style/direction %q", mark)
		}
	}
}

func TestDiagramRenderSelfMessagesKeepDashedStyle(t *testing.T) {
	for _, arrow := range []string{"-->>", "-->"} {
		t.Run(arrow, func(t *testing.T) {
			c := diagramRenderCase{source: "sequenceDiagram\nA" + arrow + "A: thinking"}
			v := c.format(100)
			c.wantRendered(t, v)
			if !strings.Contains(v.text(), "┄") {
				t.Errorf("dashed self message became solid: %s", v.text())
			}
		})
	}
}
