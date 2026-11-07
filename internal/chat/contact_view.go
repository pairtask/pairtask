package chat

import (
	"fmt"
	"maps"
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
	"not_sent":  "not sent there, kept here only",
}

// notSentReasons say why a tagged agent's reply stayed here (#278).
var notSentReasons = map[string]string{
	protocol.PeerNotShared:      "the agent isn't shared with them",
	protocol.PeerPaused:         "the workspace is paused",
	protocol.PeerContactRemoved: "the workspace was removed",
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
	direction  string
	epoch      int64
	cursor     int64
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
	thread := t.ordered()
	breaks := epochBreaks(thread)
	for i, v := range thread {
		if breaks && newEpoch(thread, i) {
			l.lines = append(l.lines, "", mutedStyle.Render("── "+epochLabel(v.PairingEpoch)+" ──"))
		}
		l.lines = append(l.lines, "") // a separator, which shows no message
		from := len(l.lines)
		l.lines = append(l.lines, m.messageLines(c, v, thread)...)
		l.spans = append(l.spans, contactSpan{id: v.ID, direction: v.Direction, epoch: v.PairingEpoch, cursor: v.Cursor, from: from, upto: len(l.lines),
			unread: v.Direction == "in" && unreadHere(v)})
	}
	l.lines = append(l.lines, m.cardLines(contactKey(fingerprint))...) // what the operator just did, below the messages
	return l
}

// contactShowsMessages: the broker carries them, and both sides confirmed
// or this side unpaired it, keeping its history.
func (m *Model) contactShowsMessages(c protocol.PairingContactView) bool {
	return m.contactsAvailable() && m.historyContact(c.IdentityKey)
}

// standsAs is how the card's title says the contact stands.
func standsAs(c protocol.PairingContactView) string {
	switch {
	case unpairedHere(c):
		return unpairedNote(c)
	case c.Confirmed:
		return "confirmed on both sides"
	}
	return "confirming: the peer has not yet confirmed the decision"
}

func (m *Model) contactHeader(c protocol.PairingContactView) []string {
	lines := m.contactCardLines(c)
	if standing := standingLine(c); standing != "" {
		lines = append(lines, warnStyle.Render("  "+standing))
	}
	return lines
}

// contactCardLines are the card's title, key and how it syncs.
func (m *Model) contactCardLines(c protocol.PairingContactView) []string {
	state := standsAs(c)
	lines := []string{
		titleStyle.Render("⇄ "+safe(contactDisplay(c))) + mutedStyle.Render(" · "+state),
		mutedStyle.Render("  identity " + safe(c.IdentityKey)),
	}
	switch {
	case !m.contactsAvailable():
		lines = append(lines, "  this broker doesn't carry contact messages; upgrade it")
	case unpairedHere(c):
		lines = append(lines, warnStyle.Render("  read-only: nothing is sent, acknowledged or shared; pairing again needs a fresh code"))
	case !c.Confirmed:
		lines = append(lines, "  "+contactConfirming)
	case pausedHere(c):
		lines = append(lines, warnStyle.Render("  "+pausedNote))
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

// messageLines is one message in a local message's layout (#275): its head,
// the message it answers, its text, then its status lines, controls
// escaped. A contact's text only styles: a diagram in it gets no /diagram
// command and is never drawn.
func (m *Model) messageLines(c protocol.PairingContactView, v protocol.ContactMessageView, thread []protocol.ContactMessageView) []string {
	s := sidesOf(c)
	lines := append(m.messageHead(s, v), m.replyRows(s, v, thread)...)
	text := v.Text
	switch {
	case refusedHere(v):
		text = refusalGuidance(v)
	case text == "":
		text = "(text no longer kept)"
	}
	// formatBody indents the text as a body already, as in local chat.
	lines = append(lines, m.formattedRows(lineBuilder{id: v.ID, inert: true}, text, 0)...)
	if m.pendingAvailable() {
		lines = append(lines, reviewCard(s, v)...)
	}
	return append(lines, m.messageStatusRows(s, v)...)
}

func (m *Model) outboundState(v protocol.ContactMessageView) string {
	label := stateLabels[v.State]
	if label == "" {
		label = safe(v.State)
	}
	if heldForUpgrade(v) {
		return "queued: leaves once they upgrade pairtask"
	}
	if expiredUnpaired(v) {
		return "not sent: the workspace was unpaired first"
	}
	return label + refusalNote(v)
}

// refusalNote is why a message was refused there, or stayed here.
func refusalNote(v protocol.ContactMessageView) string {
	switch {
	case v.RefusalReason == "":
		return ""
	case v.State == protocol.PeerNotSent && notSentReasons[v.RefusalReason] != "":
		return ": " + notSentReasons[v.RefusalReason]
	case v.State == "refused" || v.State == protocol.PeerNotSent:
		return " (" + safe(v.RefusalReason) + ")"
	}
	return ""
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
	var ids []string
	for _, r := range m.shownReads(fingerprint) {
		ids = append(ids, r.id)
	}
	return ids
}

// shownReads are shownUnread with the pairing each came under (#276).
func (m *Model) shownReads(fingerprint string) []readRef {
	l, start, end := m.contactWindow(fingerprint, m.layout().transcript)
	var refs []readRef
	for _, s := range l.spans {
		if s.unread && s.overlaps(start, end) {
			refs = append(refs, readRef{s.epoch, s.id})
		}
	}
	return refs
}

// overlaps: the message has a line in [start, end).
func (s contactSpan) overlaps(start, end int) bool {
	return s.from < end && s.upto > start
}

// readShown marks read the received messages drawn now, once each: those
// a read is pending, confirmed or refused for are left out. Each pairing's
// are read apart (#276), each request carrying at most ContactReadMax
// under its own operation ID.
func (m *Model) readShown(contact string) tea.Cmd {
	t := m.contacts.thread(contact)
	groups := map[int64][]string{} // unread drawn now, by the pairing each came under
	for _, r := range m.shownReads(contact) {
		if !t.settledRead(r) && !m.readPending(contactReadRef{contact, r}) {
			groups[r.epoch] = append(groups[r.epoch], r.id)
		}
	}
	var cmds []tea.Cmd
	for _, epoch := range slices.Sorted(maps.Keys(groups)) {
		cmds = append(cmds, m.sendRead(contactRead{contact: contact, epoch: epoch, ids: groups[epoch]}))
	}
	return tea.Batch(cmds...)
}

// contactRead is one read: a contact's messages of one pairing.
type contactRead struct {
	contact string
	epoch   int64
	ids     []string
}

// sendRead sends a read under a fixed ID, kept until its answer. A tagged
// pairing is named, so a pairing again meanwhile can't retarget it; an
// untagged row's read names none, the current one.
func (m *Model) sendRead(r contactRead) tea.Cmd {
	ids := r.ids[:min(len(r.ids), protocol.ContactReadMax)]
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpRead, ContactKey: r.contact,
		MessageID: uuid.NewString(), ContactRead: ids, ContactEpoch: r.epoch}
	if m.contacts.reads == nil {
		m.contacts.reads = map[string]*contactOp{}
	}
	m.contacts.reads[msg.MessageID] = &contactOp{msg: msg}
	return m.request(kindContactRead, msg, r.contact, msg.MessageID)
}

// settledRead: a read of this message was confirmed or refused already.
func (t *contactThread) settledRead(r readRef) bool {
	return t.read[r] || t.refused[r]
}

// contactReadRef is one contact's message, in its pairing.
type contactReadRef struct {
	contact string
	readRef
}

func (m *Model) readPending(r contactReadRef) bool {
	for _, op := range m.contacts.reads {
		if op.readsMessage(r) {
			return true
		}
	}
	return false
}

// readsMessage: the read names this contact's message, in its pairing.
func (op *contactOp) readsMessage(r contactReadRef) bool {
	if op.msg.ContactKey != r.contact || op.msg.ContactEpoch != r.epoch {
		return false
	}
	return slices.Contains(op.msg.ContactRead, r.id)
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
		settled[readRef{op.msg.ContactEpoch, id}] = true
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
	return m.contactMove(lineDelta(direction * max(1, m.contactRoom(l)-1)))
}

// contactRoom is how many message lines fit under the card.
func (m *Model) contactRoom(l contactLayout) int {
	return max(1, m.layout().transcript-len(l.header))
}

// lineDelta is how many lines to move a view; negative is up.
type lineDelta int

// contactMove moves a contact tab's messages by delta; at the top, older
// messages load when there are any.
func (m *Model) contactMove(delta lineDelta) tea.Cmd {
	contact := m.conv.selected.contact
	t := m.contacts.thread(contact)
	l := m.contactLines(contact)
	top := max(0, len(l.lines)-m.contactRoom(l))
	t.offset = max(0, min(top, t.offset-int(delta)))
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
