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
	ending := make([][]seqMessage, len(s.people)) // messages by the lifeline they constrain
	for _, m := range s.msgs {
		hi := max(m.from, m.to)
		if m.from == m.to {
			hi++ // a loop pushes the next lifeline
		}
		if hi < len(s.people) {
			ending[hi] = append(ending[hi], m)
		}
	}
	for i := range s.people {
		l.center[i] = l.least(i, ending[i])
		l.width = max(l.width, l.center[i]+boxRight(s.people[i].label))
	}
	for _, m := range s.msgs {
		if m.from == m.to {
			l.width = max(l.width, l.center[m.from]+selfLoop+2+m.label.cells)
		}
	}
	return l
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
		lo := min(m.from, m.to)
		if m.from == m.to {
			c = max(c, l.center[lo]+selfLoop+3+m.label.cells)
		} else {
			c = max(c, l.center[lo]+m.label.cells+4)
		}
	}
	return c
}

func drawSequence(s *seqDiagram) ([]string, error) {
	l := newSeqLayout(s)
	c, err := newCanvas(l.width, 4+2*len(s.msgs))
	if err != nil {
		return nil, err
	}
	for i, p := range s.people {
		c.box(l.center[i]-boxLeft(p.label), 0, p.label)
		c.vline(l.center[i], 2, c.h-1)
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
	lo, hi := min(from, to), max(from, to)
	c.put(lo+2, y, m.label)
	end := to // an arrowhead stops short of the target's lifeline
	if m.arrow {
		end = to + sign(from-to)
	}
	c.hline(from, end, y+1)
	if m.dashed {
		for x := lo + 1; x < hi; x++ {
			if c.joint[y+1][x]&(up|down) == 0 {
				c.mark(x, y+1, "┄")
			}
		}
	}
	if !m.arrow {
		return
	}
	if to > from {
		c.mark(to-1, y+1, "►")
	} else {
		c.mark(to+1, y+1, "◄")
	}
}

// self draws a message to oneself as a loop to the right.
func (l *seqLayout) self(c *canvas, m seqMessage, y int) {
	x := l.center[m.from]
	c.hline(x, x+selfLoop, y)
	c.vline(x+selfLoop, y, y+1)
	c.hline(x, x+selfLoop, y+1)
	c.put(x+selfLoop+2, y, m.label)
	if m.arrow {
		c.mark(x+1, y+1, "◄")
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
