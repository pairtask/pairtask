package chat

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
)

// The operator's own acknowledgment of a paired workspace's messages
// (#287): /ack in a ⇄ tab, never by showing them. Showing a message still
// marks it read for the transport, as before; acknowledging is private and
// sends that workspace nothing. Each tab counts what waits, the footer sums
// it, and an acknowledgment whose answer was lost is resent unchanged.

const kindContactAck = "contact ack"

// noContactAcks is why /ack does nothing in a ⇄ tab on an older broker.
const noContactAcks = "this broker can't track acknowledgments in ⇄ yet"

// contactAcksAvailable: the broker serves contact op ack.
func (m *Model) contactAcksAvailable() bool {
	return m.pairing.snapshot != nil && m.pairing.snapshot.AcksAvailable
}

// ackSummary is where a contact's acknowledgments stand: the newest the
// tab heard, else the pairing snapshot's.
func (m *Model) ackSummary(contact string) protocol.ContactAckSummary {
	if t := m.contacts.threads[contact]; t != nil && t.acks != nil {
		return *t.acks
	}
	if c, ok := m.contact(contact); ok && c.Acks != nil {
		return *c.Acks
	}
	return protocol.ContactAckSummary{}
}

// contactAckTotals are the messages waiting in every ⇄ tab, and those that
// expired unacknowledged.
func (m *Model) contactAckTotals() (waiting, expired int) {
	if !m.contactAcksAvailable() {
		return 0, 0
	}
	for _, c := range m.pairing.snapshot.Contacts {
		s := m.ackSummary(c.IdentityKey)
		waiting, expired = waiting+s.Unacked, expired+s.AckExpired
	}
	return waiting, expired
}

// contactAckReminder is the footer's part for ⇄ tabs.
func (m *Model) contactAckReminder() string {
	waiting, expired := m.contactAckTotals()
	if waiting == 0 && expired == 0 {
		return ""
	}
	text := fmt.Sprintf("⇄ %d need ack · /ack in ⇄", waiting)
	if expired > 0 {
		text += fmt.Sprintf(" · %d expired", expired)
	}
	return bandStyle.Render(text)
}

// contactAckCommand is /ack in a ⇄ tab: all of this tab, up to what the
// broker last counted, or #n, a message's number here.
func (m *Model) contactAckCommand(arg argument) tea.Cmd {
	key := m.conv.selected.contact
	switch {
	case !m.contactAcksAvailable():
		m.status = noContactAcks
		return nil
	case !m.ready():
		m.status = "not connected"
		return nil
	case arg.empty():
		m.status = "pick what to acknowledge in this tab: /ack all or /ack #n"
		return nil
	case arg == "all":
		return m.ackContactAll(m.conv.selected)
	}
	n, ok := messageNumber(string(arg))
	if !ok {
		m.status = "give #n, a message's number in this tab"
		return nil
	}
	id := waitingID(m.contacts.thread(key), n)
	if id == "" {
		m.status = fmt.Sprintf("#%d isn't a message waiting for acknowledgment in this tab", n)
		return nil
	}
	return m.sendContactAck(protocol.Message{ContactKey: key, ContactAck: []string{id}})
}

// ackContactAll acknowledges a tab up to the bound its count was read with,
// so a message arriving since stays waiting.
func (m *Model) ackContactAll(key viewKey) tea.Cmd {
	s := m.ackSummary(key.contact)
	if s.Unacked == 0 || s.AckUpto == 0 {
		m.status = "nothing to acknowledge in this tab"
		return nil
	}
	return m.sendContactAck(protocol.Message{ContactKey: key.contact, ContactAckUpto: s.AckUpto})
}

// ackContacts is /ack contacts: every ⇄ tab with messages waiting, each up
// to its own counted bound.
func (m *Model) ackContacts() tea.Cmd {
	if !m.contactAcksAvailable() {
		m.status = noContactAcks
		return nil
	}
	if !m.ready() {
		m.status = "not connected"
		return nil
	}
	var cmds []tea.Cmd
	for _, c := range m.pairing.snapshot.Contacts {
		if s := m.ackSummary(c.IdentityKey); s.Unacked > 0 && s.AckUpto > 0 {
			cmds = append(cmds, m.sendContactAck(protocol.Message{ContactKey: c.IdentityKey, ContactAckUpto: s.AckUpto}))
		}
	}
	if len(cmds) == 0 {
		m.status = "nothing to acknowledge in ⇄"
		return nil
	}
	return tea.Batch(cmds...)
}

// waitingID is the ID of the loaded message numbered n that waits for
// acknowledgment, "" for none.
func waitingID(t *contactThread, n int64) string {
	for _, v := range t.rows {
		if awaitsAck(v) && v.Cursor == n {
			return v.ID
		}
	}
	return ""
}

// messageNumber is #n's n.
func messageNumber(arg string) (int64, bool) {
	digits, ok := strings.CutPrefix(arg, "#")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	return n, err == nil
}

// awaitsAck: a received message waiting for the operator's acknowledgment.
func awaitsAck(v protocol.ContactMessageView) bool {
	return v.Direction == "in" && v.AckState == protocol.AckNeeded
}

// sendContactAck sends one acknowledgment under a fixed ID and selection,
// kept until its answer, so a resend after a lost answer is the same one.
func (m *Model) sendContactAck(msg protocol.Message) tea.Cmd {
	msg.Type, msg.ContactOp, msg.MessageID = protocol.TypeOperatorContact, protocol.ContactOpAck, uuid.NewString()
	if m.contacts.acking == nil {
		m.contacts.acking = map[string]*contactOp{}
	}
	m.contacts.acking[msg.MessageID] = &contactOp{msg: msg}
	return m.request(kindContactAck, msg, msg.ContactKey, msg.MessageID)
}

// contactAckResult keeps the tab's new count; a refusal leaves every
// message as it was, still waiting.
func (m *Model) contactAckResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	op := m.contacts.acking[req.ids[1]]
	if op == nil {
		return nil
	}
	delete(m.contacts.acking, req.ids[1])
	if resp.Type != protocol.TypeOperatorContactResult || resp.ContactAcks == nil {
		m.status = "not acknowledged: " + brokerReason(resp)
		return nil
	}
	summary := *resp.ContactAcks
	t := m.contacts.thread(op.msg.ContactKey)
	t.acks = &summary
	acked := 0
	if resp.ContactAcked != nil {
		acked = *resp.ContactAcked
	}
	m.status = fmt.Sprintf("acknowledged %d in ⇄ (yours only; nothing is sent)", acked)
	// Even none newly acknowledged: a retry answers zero for what it did
	// before, and what expired meanwhile says so only on a page.
	return m.recheckAcked(op.msg)
}

// resendContactAcks resends acknowledgments whose answers were lost,
// unchanged.
func (m *Model) resendContactAcks() tea.Cmd {
	var cmds []tea.Cmd
	for id, op := range m.contacts.acking {
		if op.unknown {
			op.unknown = false
			cmds = append(cmds, m.request(kindContactAck, op.msg, op.msg.ContactKey, id))
		}
	}
	return tea.Batch(cmds...)
}

// contactAckChoices are /ack's picker rows in a ⇄ tab: all, then each
// loaded message waiting, newest first.
func (m *Model) contactAckChoices(l argLine) []string {
	if len(l.words) != 0 || !m.contactAcksAvailable() {
		return nil
	}
	var rows []string
	thread := m.contacts.thread(m.conv.selected.contact).ordered()
	for i := len(thread) - 1; i >= 0; i-- {
		if v := thread[i]; awaitsAck(v) {
			rows = append(rows, fmt.Sprintf("#%d", v.Cursor))
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return withPrefix(append([]string{"all"}, rows...), l.current)
}

// ackMark is a received message's acknowledgment in its head.
func ackMark(v protocol.ContactMessageView) string {
	switch v.AckState {
	case protocol.AckNeeded: // as a local message waiting for you shows it (#313)
		return mutedStyle.Render(fmt.Sprintf(" #%d ", v.Cursor)) + needStyle.Render(ackMarkWarn+AckNeeded)
	case protocol.AckDone:
		return mutedStyle.Render(fmt.Sprintf(" #%d ", v.Cursor)) + doneStyle.Render(ackMarkDone+AckDone)
	case protocol.AckExpired:
		return mutedStyle.Render(fmt.Sprintf(" #%d expired unacknowledged", v.Cursor))
	}
	return ""
}

// contactAckLabel is a ⇄ tab's /ack picker row.
func (m *Model) contactAckLabel(item string) string {
	if item == "all" {
		return "  all   " + mutedStyle.Render("every message waiting in this tab (yours only; nothing is sent)")
	}
	for _, v := range m.contacts.thread(m.conv.selected.contact).rows {
		if v.Direction == "in" && fmt.Sprintf("#%d", v.Cursor) == item {
			line, _, _ := strings.Cut(v.Text, "\n")
			return "  " + item + " " + mutedStyle.Render(safeText(line))
		}
	}
	return "  " + safe(item)
}

// ackSettled: an acknowledgment nothing moves back from.
func ackSettled(v protocol.ContactMessageView) bool {
	return v.AckState == protocol.AckDone || v.AckState == protocol.AckExpired
}

// recheckAcked reads again, page by page from the newest it covered, the
// loaded messages an acknowledgment selected: only the broker says which
// it acknowledged and which had expired meanwhile, and a refresh after the
// change reads only the newest page.
func (m *Model) recheckAcked(msg protocol.Message) tea.Cmd {
	t := m.contacts.thread(msg.ContactKey)
	var cursors []int64
	for _, v := range t.rows {
		if awaitsAck(v) && acks(msg, v) {
			cursors = append(cursors, v.Cursor)
		}
	}
	if len(cursors) == 0 {
		return nil
	}
	slices.Sort(cursors)
	t.recheck = cursors
	return m.refreshContact(msg.ContactKey, cursors[len(cursors)-1]+1)
}

// recheckNext drops what an older page covered from the messages being read
// again, and reads the next page while any remain below it.
func (m *Model) recheckNext(contact string, page *protocol.ContactMessagePage) tea.Cmd {
	t := m.contacts.thread(contact)
	if len(t.recheck) == 0 || len(page.Messages) == 0 {
		t.recheck = nil
		return nil
	}
	oldest := oldestCursor(page.Messages)
	t.recheck = slices.DeleteFunc(t.recheck, func(c int64) bool { return c >= oldest })
	if len(t.recheck) == 0 || !page.More {
		t.recheck = nil
		return nil
	}
	return m.refreshContact(contact, oldest)
}

// acks: the acknowledgment selects the message.
func acks(msg protocol.Message, v protocol.ContactMessageView) bool {
	if msg.ContactAckUpto != 0 {
		return v.Cursor <= msg.ContactAckUpto
	}
	return slices.Contains(msg.ContactAck, v.ID)
}
