package chat

import (
	"fmt"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
)

// contactConfirming is why a contact tab sends nothing yet.
const contactConfirming = "this workspace isn't confirmed on both sides yet; nothing is sent until it is"

// stateLabels say what a message's state means, and nothing more: queued
// is no confirmed delivery yet, expired is no longer offered.
var stateLabels = map[string]string{
	"queued":    "queued: leaves at the next sync",
	"delivered": "delivered",
	"read":      "read",
	"refused":   "refused",
	"expired":   "expired: no longer offered; delivery unconfirmed",
}

// contactLayout is a contact tab: its card, which stays on top, and the
// message lines below it with, for each received message, the lines it
// occupies.
type contactLayout struct {
	header []string
	lines  []string
	spans  []contactSpan
}

type contactSpan struct {
	id         string
	from, upto int // message lines [from, upto)
	unread     bool
}

// contactLines lays out a contact tab at the terminal's width: the card and
// sync line, then the messages oldest first.
func (m *Model) contactLines(fingerprint string) contactLayout {
	c, _ := m.contact(fingerprint)
	l := contactLayout{header: m.contactHeader(c)}
	t := m.contacts.threads[fingerprint]
	if t == nil || !m.contactShowsMessages(c) {
		return l
	}
	for _, v := range t.ordered() {
		from := len(l.lines)
		l.lines = append(l.lines, m.messageLines(c, v)...)
		if v.Direction == "in" {
			l.spans = append(l.spans, contactSpan{id: v.ID, from: from, upto: len(l.lines), unread: v.State == "received"})
		}
	}
	return l
}

// contactShowsMessages: the broker carries them and both sides confirmed.
func (m *Model) contactShowsMessages(c protocol.PairingContactView) bool {
	return m.contactsAvailable() && c.Confirmed
}

func (m *Model) contactHeader(c protocol.PairingContactView) []string {
	state := "confirming: the peer has not yet confirmed the decision"
	if c.Confirmed {
		state = "confirmed on both sides"
	}
	lines := []string{
		titleStyle.Render("⇄ "+safe(contactDisplay(c))) + mutedStyle.Render(" · "+state),
		mutedStyle.Render("  identity " + safe(c.IdentityKey)),
	}
	switch {
	case !m.contactsAvailable():
		lines = append(lines, "  this broker doesn't carry contact messages; upgrade it")
	case !c.Confirmed:
		lines = append(lines, "  "+contactConfirming)
	default:
		lines = append(lines, mutedStyle.Render("  "+m.syncLine(c.IdentityKey)))
		if share := sharingLine(m.contacts.threads[c.IdentityKey]); share != "" && m.routingAvailable() {
			lines = append(lines, mutedStyle.Render("  "+share))
		}
		if grants := reviewGrantsLine(m.contacts.threads[c.IdentityKey]); grants != "" {
			lines = append(lines, mutedStyle.Render("  "+grants))
		}
	}
	return lines
}

// departureNote says when a message written now leaves: at once over a
// live link or when this side dials, else when the other side next syncs.
func (m *Model) departureNote(key viewKey) string {
	if m.leavesAtOnce(key) {
		return "sent at once"
	}
	return "leaves when they next sync (30 s while active, else 5 min)"
}

// leavesAtOnce: a live link is open, or this side dials, as its loaded
// thread says.
func (m *Model) leavesAtOnce(key viewKey) bool {
	t := m.contacts.threads[key.contact]
	if t == nil || !t.loaded {
		return false
	}
	return t.sync.Live || t.sync.Dials
}

// syncLine is what this side knows of syncing with the contact.
func (m *Model) syncLine(fingerprint string) string {
	t := m.contacts.threads[fingerprint]
	if t == nil || !t.loaded {
		return "loading messages…"
	}
	s := t.sync
	line := "this side waits to be dialed; messages leave at the contact's next sync"
	switch {
	case s.Live:
		line = "connected: messages leave at once, both ways"
	case s.Dials:
		c, _ := m.contact(fingerprint)
		line = "this side dials" + dialedAt(c)
	}
	if s.LastSuccess != nil {
		line += " · last sync " + s.LastSuccess.Local().Format(time.Kitchen)
	}
	if s.LastFailure != "" {
		line += fmt.Sprintf(" · last attempt failed: %s (%d in a row)", safe(s.LastFailure), s.Failures)
	}
	return line
}

// dialedAt is " <address>" this side dials the contact at, or "".
func dialedAt(c protocol.PairingContactView) string {
	if c.Destination == "" {
		return ""
	}
	return " " + safe(c.Destination)
}

// messageLines is one message: who and its state, then its text laid out
// like a local message's, controls escaped. A contact's text only styles:
// a diagram in it gets no /diagram command and is never drawn.
func (m *Model) messageLines(c protocol.PairingContactView, v protocol.ContactMessageView) []string {
	at := v.CreatedAt.Local().Format("15:04")
	head := agentStyle(c.IdentityKey).Render("  ⇄ "+safe(contactDisplay(c))) + mutedStyle.Render(routeHead(v)+" · "+at)
	if v.Direction == "out" {
		head = titleStyle.Render("  you →") + mutedStyle.Render(routeHead(v)+" · "+at+" · "+m.outboundState(v))
	}
	lines := []string{head}
	text := v.Text
	if text == "" {
		text = "(text no longer kept)"
	}
	return append(lines, m.formattedRows(lineBuilder{id: v.ID, inert: true}, text, len(body))...)
}

func (m *Model) outboundState(v protocol.ContactMessageView) string {
	label := stateLabels[v.State]
	if label == "" {
		label = safe(v.State)
	}
	if heldForUpgrade(v) {
		return "queued: leaves once they upgrade pairtask"
	}
	if v.State == "refused" && v.RefusalReason != "" {
		label += " (" + safe(v.RefusalReason) + ")"
	}
	return label
}

// heldForUpgrade: still queued, and held only until the contact upgrades;
// an expired one will never leave.
func heldForUpgrade(v protocol.ContactMessageView) bool {
	return v.State == "queued" && v.RefusalReason == protocol.ContactHeldForUpgrade
}

// ordered is the thread oldest first by local cursor.
func (t *contactThread) ordered() []protocol.ContactMessageView {
	out := make([]protocol.ContactMessageView, 0, len(t.rows))
	for _, v := range t.rows {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b protocol.ContactMessageView) int { return int(a.Cursor - b.Cursor) })
	return out
}

// contactWindow is the message lines drawn below the card in rows: the
// newest, scrolled up by the thread's offset.
func (m *Model) contactWindow(fingerprint string, rows int) (contactLayout, int, int) {
	l := m.contactLines(fingerprint)
	room := max(0, rows-len(l.header))
	end := len(l.lines)
	if t := m.contacts.threads[fingerprint]; t != nil {
		end = max(min(room, len(l.lines)), len(l.lines)-t.offset)
	}
	return l, max(0, end-room), end
}

// contactCard fills a contact tab: the card on top, then the message lines
// that fit, newest last.
func (m *Model) contactCard(key viewKey, rows int) []string {
	l, start, end := m.contactWindow(key.contact, rows)
	lines := append(slices.Clone(l.header), l.lines[start:end]...)
	return lines[:min(len(lines), rows)]
}

// shownUnread are the received messages drawn now in the open tab, wholly
// or in part; offscreen ones are not.
func (m *Model) shownUnread(fingerprint string) []string {
	l, start, end := m.contactWindow(fingerprint, m.layout().transcript)
	var ids []string
	for _, s := range l.spans {
		if s.unread && s.overlaps(start, end) {
			ids = append(ids, s.id)
		}
	}
	return ids
}

// overlaps: the message has a line in [start, end).
func (s contactSpan) overlaps(start, end int) bool {
	return s.from < end && s.upto > start
}

// readShown marks read the received messages drawn now, once each: IDs a
// read is pending, confirmed or refused for are left out, and each request
// carries at most ContactReadMax under its own operation ID.
func (m *Model) readShown(contact string) tea.Cmd {
	t := m.contacts.thread(contact)
	var ids []string
	for _, id := range m.shownUnread(contact) {
		if !t.settledRead(id) && !m.readPending(contact, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	ids = ids[:min(len(ids), protocol.ContactReadMax)]
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpRead, ContactKey: contact,
		MessageID: uuid.NewString(), ContactRead: ids}
	if m.contacts.reads == nil {
		m.contacts.reads = map[string]*contactOp{}
	}
	m.contacts.reads[msg.MessageID] = &contactOp{msg: msg}
	return m.request(kindContactRead, msg, contact, msg.MessageID)
}

// settledRead: a read of this message was confirmed or refused already.
func (t *contactThread) settledRead(id string) bool {
	return t.read[id] || t.refused[id]
}

func (m *Model) readPending(contact, id string) bool {
	for _, op := range m.contacts.reads {
		if op.msg.ContactKey == contact && slices.Contains(op.msg.ContactRead, id) {
			return true
		}
	}
	return false
}

// contactReadResult settles a read: its IDs are confirmed, or refused and
// not asked again until the thread reloads; the page it carries merges.
func (m *Model) contactReadResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	op := m.contacts.reads[req.ids[1]]
	if op == nil {
		return nil
	}
	delete(m.contacts.reads, req.ids[1])
	t := m.contacts.thread(op.msg.ContactKey)
	settled := t.refused
	if resp.Type == protocol.TypeOperatorContactResult {
		settled = t.read
	}
	for _, id := range op.msg.ContactRead {
		settled[id] = true
	}
	if p := resp.ContactPage; p != nil && p.Contact == op.msg.ContactKey {
		t.merge(p.Messages)
	}
	return nil
}

// contactScroll moves a contact tab's messages by a page under its card;
// at the top, older messages load when there are any.
func (m *Model) contactScroll(direction int) tea.Cmd {
	l := m.contactLines(m.conv.selected.contact)
	return m.contactMove(direction * max(1, m.contactRoom(l)-1))
}

// contactRoom is how many message lines fit under the card.
func (m *Model) contactRoom(l contactLayout) int {
	return max(1, m.layout().transcript-len(l.header))
}

// contactMove moves a contact tab's messages by delta lines (negative: up);
// at the top, older messages load when there are any.
func (m *Model) contactMove(delta int) tea.Cmd {
	contact := m.conv.selected.contact
	t := m.contacts.thread(contact)
	l := m.contactLines(contact)
	top := max(0, len(l.lines)-m.contactRoom(l))
	t.offset = max(0, min(top, t.offset-delta))
	if delta < 0 && m.olderDue(contact, t, top) {
		return m.refreshContact(contact, t.oldest)
	}
	return nil
}

// olderDue: scrolled to the top of what is held, with older messages left
// and none being read.
func (m *Model) olderDue(contact string, t *contactThread, top int) bool {
	if t.offset != top || !t.more {
		return false
	}
	return !m.requested(kindContactList, contact)
}
