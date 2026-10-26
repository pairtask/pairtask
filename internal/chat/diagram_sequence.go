package chat

// seqDiagram is a parsed sequence diagram: participants in order of first
// appearance and the messages between them.
type seqDiagram struct {
	people []seqPerson
	index  map[string]int
	msgs   []seqMessage
}

type seqPerson struct {
	label    diagramLabel
	declared bool
}

type seqMessage struct {
	from, to int
	label    diagramLabel
	dashed   bool
	arrow    bool
}

func parseSequence(statements []string) (*seqDiagram, error) {
	s := &seqDiagram{index: map[string]int{}}
	for _, st := range statements {
		if err := s.statement(st); err != nil {
			return nil, err
		}
	}
	if len(s.msgs) == 0 {
		return nil, errUnsupported
	}
	return s, nil
}

func (s *seqDiagram) statement(st string) error {
	if m := participantRe.FindStringSubmatchIndex(st); m != nil {
		raw, present := sub(st, m, 2)
		label, err := optionalLabel(raw, present)
		if err != nil {
			return err
		}
		_, err = s.person(st[m[2]:m[3]], label, present)
		return err
	}
	m := messageRe.FindStringSubmatch(st)
	if m == nil || len(s.msgs) == maxMessages {
		return errUnsupported
	}
	return s.message(m[1], m[2], m[3], m[4])
}

func (s *seqDiagram) message(from, arrow, to, text string) error {
	label, err := parseLabel(text)
	if err != nil {
		return err
	}
	f, err := s.person(from, diagramLabel{}, false)
	if err != nil {
		return err
	}
	t, err := s.person(to, diagramLabel{}, false)
	if err != nil {
		return err
	}
	s.msgs = append(s.msgs, seqMessage{from: f, to: t, label: label,
		dashed: arrow == "-->>" || arrow == "-->", arrow: arrow == "->>" || arrow == "-->>"})
	return nil
}

// person finds or adds a participant; a second, different label is a
// conflict.
func (s *seqDiagram) person(id string, label diagramLabel, declared bool) (int, error) {
	i, ok := s.index[id]
	if !ok {
		if len(s.people) == maxParticipants {
			return 0, errUnsupported
		}
		i = len(s.people)
		s.index[id] = i
		s.people = append(s.people, seqPerson{label: diagramLabel{text: id, cells: len(id)}})
	}
	p := &s.people[i]
	switch {
	case !declared:
	case p.declared && p.label != label:
		return 0, errUnsupported
	default:
		p.label, p.declared = label, true
	}
	if p.label.cells > maxLabelCells {
		return 0, errUnsupported
	}
	return i, nil
}

// seqLayout spaces the lifelines left to right, each as far as its box and
// the labels of the messages that end at it need; one pass over the
// participants and one over the messages.
type seqLayout struct {
	s      *seqDiagram
	center []int
	width  int
}

const (
	lifelineGap = 2 // between neighbouring boxes
	selfLoop    = 4 // a message to oneself turns this far right
)

func newSeqLayout(s *seqDiagram) *seqLayout {
	l := &seqLayout{s: s, center: make([]int, len(s.people))}
	ending := s.byLifeline()
	for i := range s.people {
		l.center[i] = l.least(i, ending[i])
		l.width = max(l.width, l.center[i]+boxRight(s.people[i].label))
	}
	for _, m := range s.msgs {
		l.width = max(l.width, l.loopEnd(m))
	}
	return l
}

// byLifeline groups the messages by the lifeline whose position they
// constrain: the right end of a message, or the one after a loop.
func (s *seqDiagram) byLifeline() [][]seqMessage {
	ending := make([][]seqMessage, len(s.people)+1)
	for _, m := range s.msgs {
		hi := max(m.from, m.to)
		if m.from == m.to {
			hi++
		}
		ending[hi] = append(ending[hi], m)
	}
	return ending
}

// loopEnd is how far right a message to oneself reaches, label included.
func (l *seqLayout) loopEnd(m seqMessage) int {
	if m.from != m.to {
		return 0
	}
	return l.center[m.from] + selfLoop + 2 + m.label.cells
}

// boxLeft and boxRight are a box's extent either side of its centre.
func boxLeft(l diagramLabel) int  { return (l.cells + 4) / 2 }
func boxRight(l diagramLabel) int { return l.cells + 4 - boxLeft(l) }

// least is the smallest centre for lifeline i: clear of the previous box,
// and wide enough for every message label that ends at i or loops at i-1.
func (l *seqLayout) least(i int, ending []seqMessage) int {
	p := l.s.people[i].label
	if i == 0 {
		return boxLeft(p)
	}
	c := l.center[i-1] + boxRight(l.s.people[i-1].label) + lifelineGap + boxLeft(p)
	for _, m := range ending {
		c = max(c, l.center[min(m.from, m.to)]+m.label.cells+l.labelRoom(m))
	}
	return c
}

// labelRoom is the space a message needs beside its label.
func (l *seqLayout) labelRoom(m seqMessage) int {
	if m.from == m.to {
		return selfLoop + 3
	}
	return 4
}

func drawSequence(s *seqDiagram) ([]string, error) {
	l := newSeqLayout(s)
	c, err := newCanvas(gridPos{l.width, 4 + 2*len(s.msgs)})
	if err != nil {
		return nil, err
	}
	for i, p := range s.people {
		c.box(gridPos{l.center[i] - boxLeft(p.label), 0}, p.label)
		c.line(gridPos{l.center[i], 2}, gridPos{l.center[i], c.size.y - 1})
	}
	for i, m := range s.msgs {
		l.message(c, m, 3+2*i)
	}
	return c.rows(), nil
}

// message draws a label row and an arrow row.
func (l *seqLayout) message(c *canvas, m seqMessage, y int) {
	if m.from == m.to {
		l.self(c, m, y)
		return
	}
	from, to := l.center[m.from], l.center[m.to]
	c.put(gridPos{min(from, to) + 2, y}, m.label)
	head := gridPos{to + sign(from-to), y + 1} // an arrowhead stops short of the lifeline
	end := gridPos{to, y + 1}
	if m.arrow {
		end = head
	}
	c.line(gridPos{from, y + 1}, end)
	if m.dashed {
		dash(c, gridPos{min(from, to), y + 1}, max(from, to))
	}
	if m.arrow {
		c.mark(head, arrowhead(to > from))
	}
}

func arrowhead(rightward bool) string {
	if rightward {
		return "►"
	}
	return "◄"
}

// self draws a message to oneself as a loop to the right.
func (l *seqLayout) self(c *canvas, m seqMessage, y int) {
	at := gridPos{l.center[m.from], y}
	turn := at.right(selfLoop)
	c.line(at, turn)
	c.line(turn, turn.down(1))
	c.line(at.down(1), turn.down(1))
	c.put(turn.right(2), m.label)
	if m.dashed {
		dash(c, at, turn.x)
		dash(c, at.down(1), turn.x)
	}
	if m.arrow {
		c.mark(at.right(1).down(1), arrowhead(false))
	}
}

// dash marks the line cells after from and before column to as dashed,
// leaving the lifelines it crosses joined.
func dash(c *canvas, from gridPos, to int) {
	for p := from.right(1); p.x < to; p = p.right(1) {
		if !c.vertical(p) {
			c.mark(p, "┄")
		}
	}
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}
