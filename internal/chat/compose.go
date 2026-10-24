package chat

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// composerMaxLines bounds the composer's height on screen: it grows with the
// draft, soft-wrapped rows included, up to this many rows and scrolls inside
// after that.
const composerMaxLines = 6

// composerMaxContentLines keeps the editor's content guard out of the way: the
// draft is bounded by the payload limit at send time, not by its row count.
const composerMaxContentLines = 1 << 20

// composer is the single input: the draft, its explicit audience and reply,
// the command line and the open picker.
type composer struct {
	editor textarea.Model
	width  int          // the outer width the editor was last sized to
	pasted bool         // the draft began with pasted text: literal, never parsed
	to     []string     // /to audience
	reply  *replyTarget // frozen reply target
	cmd    *commandLine
	picker *picker
}

// replyTarget is frozen from a received entry when /reply runs.
type replyTarget struct {
	messageID string
	sender    string
	seq       uint64
}

// commandLine is the separate input for commands; it never touches the draft.
type commandLine struct {
	input  textinput.Model
	pasted bool // pasted text is inert: Enter refuses it
}

// Picker kinds.
const (
	PickerAgent   = "agent"
	PickerCommand = "command"
)

type picker struct {
	kind     string
	items    []string
	selected int
}

func newComposer() composer {
	editor := textarea.New()
	editor.ShowLineNumbers = false
	editor.Prompt = "› "
	editor.Placeholder = "Message everyone · @agent for one · / for commands"
	editor.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "alt+enter", "ctrl+j"))
	editor.DynamicHeight, editor.MinHeight, editor.MaxHeight = true, 1, composerMaxLines
	editor.MaxContentHeight = composerMaxContentLines
	editor.Focus()
	return composer{editor: editor}
}

// setWidth sizes the editor to an outer width, once per change: the editor
// reports its inner width, so the outer one is remembered here.
func (c *composer) setWidth(outer int) {
	if c.width == outer {
		return
	}
	c.width = outer
	c.editor.SetWidth(outer)
}

// draftEmpty reports an empty draft (whitespace included).
func (c *composer) draftEmpty() bool {
	return c.editor.Value() == ""
}

// setDraft replaces the text, keeping whether it was pasted: pasted text
// stays literal wherever it moves.
func (c *composer) setDraft(text string, pasted bool) {
	c.editor.SetValue(text)
	c.editor.CursorEnd()
	c.pasted = pasted && text != ""
}

// resetDraft clears the draft and the metadata that belongs to it.
func (c *composer) resetDraft() {
	c.editor.Reset()
	c.pasted, c.to, c.reply, c.picker = false, nil, nil, nil
}

// editorKey handles a key while the draft has the input.
func (m *Model) editorKey(msg tea.KeyPressMsg) tea.Cmd {
	c := &m.compose
	if c.picker != nil {
		if cmd, handled := m.pickerKey(msg); handled {
			return cmd
		}
	}
	if cmd, handled := m.draftKey(msg); handled {
		return cmd
	}
	if msg.Text == "/" && c.draftEmpty() {
		m.openCommandLine()
		return nil
	}
	m.endRecall() // any edit leaves an ordinary draft
	cmd := m.updateEditor(msg)
	m.afterEdit()
	return cmd
}

// draftKey handles the keys the editor never sees: Enter sends, Esc ends a
// recall (a reply is cancelled only by /reply cancel), Tab cycles views, and
// Up/Down walk sent messages while a recall owns the draft.
func (m *Model) draftKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "enter":
		return m.enter(), true
	case "esc":
		m.endRecall()
		return nil, true
	case "tab", "shift+tab":
		m.switchView(msg.String())
		return nil, true
	case "up":
		return nil, m.recallUp()
	case "down":
		return nil, m.recallDown()
	}
	return nil, false
}

// switchView cycles the conversation bar from the editor: Tab forward,
// Shift+Tab back. A picker owns these keys first.
func (m *Model) switchView(key string) {
	m.refreshDirectory()
	if key == "tab" {
		m.cycleView(1)
		return
	}
	m.cycleView(-1)
}

// enter sends, or turns a trailing backslash into a line break.
func (m *Model) enter() tea.Cmd {
	c := &m.compose
	value := c.editor.Value()
	if strings.HasSuffix(value, `\`) && c.editor.Line() == c.editor.LineCount()-1 {
		c.editor.SetValue(strings.TrimSuffix(value, `\`) + "\n")
		m.afterEdit()
		return nil
	}
	return m.submit()
}

func (m *Model) updateEditor(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.compose.editor, cmd = m.compose.editor.Update(msg)
	return cmd
}

// afterEdit refreshes the draft's derived state: the paste flag resets on an
// empty draft and the mention picker follows the cursor's token. The editor
// sizes itself to its visual rows.
func (m *Model) afterEdit() {
	c := &m.compose
	if c.draftEmpty() {
		c.pasted = false
	}
	m.refreshAgentPicker()
}

// paste inserts pasted text as one insertion; it never sends, runs a
// command or picks anything.
func (m *Model) paste(msg tea.PasteMsg) tea.Cmd {
	c := &m.compose
	if c.cmd != nil {
		c.cmd.pasted = true
		var cmd tea.Cmd
		c.cmd.input, cmd = c.cmd.input.Update(msg)
		return cmd
	}
	if c.draftEmpty() {
		c.pasted = true
	}
	c.picker = nil
	m.endRecall() // pasting edits the draft
	return m.updateEditor(msg)
}

// refreshAgentPicker opens the @ picker while the cursor is in an unfinished
// leading mention, and closes it otherwise.
func (m *Model) refreshAgentPicker() {
	c := &m.compose
	fragment, ok := m.mentionFragment()
	if !ok {
		c.closePicker(PickerAgent)
		return
	}
	items := m.agentCandidates(fragment)
	if len(items) == 0 {
		c.picker = nil
		return
	}
	selected := 0
	if p := c.openPicker(PickerAgent); p != nil && p.selected < len(items) {
		selected = p.selected
	}
	c.picker = &picker{kind: PickerAgent, items: items, selected: selected}
}

// openPicker is the open picker of kind, or nil.
func (c *composer) openPicker(kind string) *picker {
	if c.picker != nil && c.picker.kind == kind {
		return c.picker
	}
	return nil
}

func (c *composer) closePicker(kind string) {
	if c.openPicker(kind) != nil {
		c.picker = nil
	}
}

// pickerKey handles keys while a picker is open. Tab and Enter only complete.
func (m *Model) pickerKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	p := m.compose.picker
	switch msg.String() {
	case "up":
		p.selected = (p.selected - 1 + len(p.items)) % len(p.items)
	case "down":
		p.selected = (p.selected + 1) % len(p.items)
	case "enter":
		if p.kind == PickerAck {
			return m.pickAck(p.items[p.selected]), true
		}
		m.complete(p.items[p.selected])
	case "tab":
		if p.kind != PickerAck {
			m.complete(p.items[p.selected])
		}
	case "esc":
		m.compose.picker = nil
	default:
		return nil, false
	}
	return nil, true
}

// complete replaces the typed "@fragment" with the picked ID. An ID that
// can't be written as a mention becomes the /to audience instead.
func (m *Model) complete(id string) {
	c := &m.compose
	// An agent named "all" must never become the everyone keyword.
	if id == "all" || !mentionID.MatchString(id) {
		m.pickLiteral(id)
		return
	}
	value := c.editor.Value()
	cut := strings.LastIndex(value, "@")
	c.editor.SetValue(value[:cut] + "@" + id + " ")
	c.editor.CursorEnd()
	c.picker = nil
	m.afterEdit()
}

// pickLiteral sets a /to audience for an ID that isn't a valid mention.
func (m *Model) pickLiteral(id string) {
	c := &m.compose
	value := c.editor.Value()
	before := value[:strings.LastIndex(value, "@")]
	c.picker = nil
	if strings.TrimSpace(before) != "" {
		m.status = "remove the other mentions to address " + safe(id)
		return
	}
	if c.reply != nil {
		m.status = "use /reply cancel first"
		return
	}
	c.editor.SetValue(before)
	c.editor.CursorEnd()
	c.to = []string{id}
	m.afterEdit()
}

// openCommandLine starts a command; the draft is kept as it is.
func (m *Model) openCommandLine() {
	input := textinput.New()
	input.Prompt = "/"
	input.SetWidth(m.compose.width - 2)
	input.Focus()
	m.compose.cmd = &commandLine{input: input}
	m.compose.picker = nil
	m.refreshCommandPicker()
}

func (m *Model) closeCommandLine() {
	m.compose.cmd = nil
	m.compose.picker = nil
}

// commandKey handles a key on the command line.
func (m *Model) commandKey(msg tea.KeyPressMsg) tea.Cmd {
	if cmd, handled := m.commandControl(msg); handled {
		return cmd
	}
	line := m.compose.cmd
	if msg.Text == "/" && line.input.Value() == "" {
		m.slashDraft()
		return nil
	}
	var cmd tea.Cmd
	line.input, cmd = line.input.Update(msg)
	m.refreshCommandPicker()
	return cmd
}

func (m *Model) commandControl(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		m.closeCommandLine()
	case "enter":
		if m.compose.picker != nil {
			return m.runPicked(), true
		}
		return m.runCommandLine(), true
	case "tab":
		m.completeCommand()
	case "up", "down":
		if m.compose.picker != nil {
			m.pickerKey(msg)
		}
	default:
		return nil, false
	}
	return nil, true
}

// slashDraft handles "//": it leaves command mode and starts a draft
// beginning with "/".
func (m *Model) slashDraft() {
	c := &m.compose
	m.closeCommandLine()
	if c.draftEmpty() {
		c.editor.SetValue("/")
		c.editor.CursorEnd()
	}
}

func (m *Model) refreshCommandPicker() {
	c := &m.compose
	name, _, hasArgs := strings.Cut(c.cmd.input.Value(), " ")
	if hasArgs {
		c.picker = nil
		return
	}
	items := m.commandCandidates(argument(name))
	if len(items) == 0 {
		c.picker = nil
		return
	}
	c.picker = &picker{kind: PickerCommand, items: items}
}

// completeCommand inserts the selected command name; it never runs it.
func (m *Model) completeCommand() {
	c := &m.compose
	if c.picker == nil || c.picker.kind != PickerCommand {
		return
	}
	c.cmd.input.SetValue(c.picker.items[c.picker.selected] + " ")
	c.cmd.input.CursorEnd()
	c.picker = nil
}

// runPicked completes the highlighted command and runs it when it runs
// without an argument; a command that needs one stays on the line, completed,
// waiting for it.
func (m *Model) runPicked() tea.Cmd {
	if m.compose.cmd.pasted {
		return m.runCommandLine() // refused: pasted text never runs
	}
	m.completeCommand()
	name := strings.TrimSpace(m.compose.cmd.input.Value())
	if byName(name).check("") != nil {
		return nil
	}
	return m.runCommand(argument(name), "")
}

// key routes a key press: global keys, then the help panel, then the
// command line, then the draft.
func (m *Model) key(msg tea.KeyPressMsg) tea.Cmd {
	if cmd, handled := m.globalKey(msg); handled {
		return cmd
	}
	if m.compose.cmd != nil {
		return m.commandKey(msg)
	}
	if m.panel != nil {
		if msg.String() == "esc" {
			m.panel = nil
		}
		return nil
	}
	return m.editorKey(msg)
}

// globalKeys act in every input mode.
var globalKeys = map[string]func(*Model) tea.Cmd{
	"ctrl+q":   (*Model).quit,
	"ctrl+c":   (*Model).quit,
	"f1":       func(m *Model) tea.Cmd { m.toggleHelp(); return nil },
	"ctrl+k":   func(m *Model) tea.Cmd { m.ensureCommandLine(); return nil },
	"ctrl+r":   (*Model).ctrlR,
	"pgup":     func(m *Model) tea.Cmd { return m.page(-1) },
	"pgdown":   func(m *Model) tea.Cmd { return m.page(1) },
	"ctrl+end": func(m *Model) tea.Cmd { m.follow(); return nil },
}

func (m *Model) globalKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	key := msg.String()
	if handle := globalKeys[key]; handle != nil {
		return handle(m), true
	}
	if position, ok := altDigit(key); ok {
		m.refreshDirectory()
		m.jumpView(position)
		return nil, true
	}
	return nil, false
}

// altDigit reads Alt+1..9 as a bar position.
func altDigit(key string) (int, bool) {
	digit, ok := strings.CutPrefix(key, "alt+")
	if !ok || len(digit) != 1 {
		return 0, false
	}
	position := int(digit[0] - '0')
	return position, position >= 1 && position <= 9
}

func (m *Model) ensureCommandLine() {
	if m.compose.cmd == nil {
		m.openCommandLine()
	}
}

// page scrolls an open panel, or the transcript.
func (m *Model) page(direction int) tea.Cmd {
	if m.panel != nil {
		m.panel.scroll(direction, m.layout().transcript)
		return nil
	}
	return m.scrollPage(direction)
}

// ctrlR retries a stopped connection, otherwise replies to the newest
// message to the operator.
func (m *Model) ctrlR() tea.Cmd {
	if m.conn == OwnerBusy || m.conn == Stopped {
		return m.manualRetry()
	}
	m.replyCommand("")
	return nil
}

// manualRetry reconnects after owner_busy on reconnect, on request only.
func (m *Model) manualRetry() tea.Cmd {
	m.conn = Reconnecting
	m.status = "retrying"
	return m.dial()
}

func (m *Model) resize(size tea.WindowSizeMsg) {
	m.width, m.height = size.Width, size.Height
	m.compose.setWidth(m.editorWidth(m.width >= boxMinWidth))
	for _, v := range m.conv.saved {
		v.compose.setWidth(m.editorWidth(m.width >= boxMinWidth))
	}
}
