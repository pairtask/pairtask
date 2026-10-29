package chat

import (
	"maps"
	"slices"
	"strconv"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Messages with a paired workspace (#210), in its ⇄ tab: the operator writes
// to that workspace's operator and reads what came back. Everything a
// contact sent is text from that contact, shown escaped: never a command, a
// mention or anything the broker or an agent acts on.

const (
	kindContactList = "contact list"
	kindContactSend = "contact send"
	kindContactRead = "contact read"
)

// contactThread is what chat holds of one contact's messages.
type contactThread struct {
	rows    map[string]protocol.ContactMessageView // by direction and ID
	loaded  bool                                   // the newest page arrived at least once
	failed  bool                                   // the last read was refused; no retry until an event, a reconnect or reopening the tab
	stale   bool                                   // changed while a refresh was in flight
	gen     uint64                                 // the newest refresh asked for
	oldest  int64                                  // the cursor below which older pages lie
	more    bool                                   // older messages exist below oldest
	sync    protocol.ContactSyncView
	offset  int             // rows scrolled up from the newest
	read    map[string]bool // inbound IDs a read was confirmed for
	refused map[string]bool // inbound IDs a read was refused for; not asked again until reloaded
}

// contactOp is a send or a read whose ID and content are fixed when made,
// so a resend after a lost answer is the same operation.
type contactOp struct {
	msg     protocol.Message
	unknown bool // the answer was lost; resent after the reconnect
}

// contactState is every contact's thread and the operations in flight.
type contactState struct {
	threads map[string]*contactThread
	send    *contactOp            // one send at a time
	reads   map[string]*contactOp // by operation ID
}

func (s *contactState) thread(contact string) *contactThread {
	if s.threads == nil {
		s.threads = map[string]*contactThread{}
	}
	t := s.threads[contact]
	if t == nil {
		t = &contactThread{rows: map[string]protocol.ContactMessageView{}, read: map[string]bool{}, refused: map[string]bool{}}
		s.threads[contact] = t
	}
	return t
}

func rowKey(v protocol.ContactMessageView) string { return v.Direction + "/" + v.ID }

// contactsAvailable: the broker serves contact messages.
func (m *Model) contactsAvailable() bool {
	return m.session.HasCapability(protocol.CapabilityContactMessages)
}

// confirmedContact: a contact both sides confirmed, the only kind messages
// go to or are read from.
func (m *Model) confirmedContact(fingerprint string) bool {
	c, ok := m.contact(fingerprint)
	return ok && c.Confirmed
}

// contactFollowUp runs after every update: it loads the open contact tab's
// messages once, and marks read what the operator now sees there.
func (m *Model) contactFollowUp() tea.Cmd {
	key := m.conv.selected
	if !m.contactTabShown(key) || !m.contactsUsable(key.contact) {
		return nil
	}
	t := m.contacts.thread(key.contact)
	var cmds []tea.Cmd
	if !t.loaded && !t.failed && !m.requested(kindContactList, key.contact) {
		cmds = append(cmds, m.refreshContact(key.contact, 0))
	}
	return tea.Batch(append(cmds, m.readShown(key.contact))...)
}

// contactTabShown: a contact tab is open and nothing covers it.
func (m *Model) contactTabShown(key viewKey) bool {
	return key.isContact() && m.panel == nil
}

// contactsUsable: connected to a broker that carries contact messages, for
// a confirmed contact.
func (m *Model) contactsUsable(contact string) bool {
	if !m.ready() || !m.contactsAvailable() {
		return false
	}
	return m.confirmedContact(contact)
}

// refreshContact reads a page of a contact's messages: the newest (before
// 0) or an older one.
func (m *Model) refreshContact(contact string, before int64) tea.Cmd {
	t := m.contacts.thread(contact)
	if before == 0 {
		t.gen++
	}
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: contact, ContactBefore: before}
	return m.request(kindContactList, msg, contact, strconv.FormatUint(t.gen, 10), strconv.FormatInt(before, 10))
}

// contactListResult merges a page into the contact's thread. Pages merge
// by direction and ID, never moving a message's state back, so a late page
// can't undo a newer one; only the newest refresh decides the sync view.
func (m *Model) contactListResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	contact, gen, before := req.ids[0], req.ids[1], req.ids[2]
	t := m.contacts.thread(contact)
	if resp.Type != protocol.TypeOperatorContactResult || resp.ContactPage == nil || resp.ContactPage.Contact != contact {
		t.failed = true // no retry on every update; an event, a reconnect or reopening the tab retries
		m.status = "contact messages not loaded: " + refusalReason(resp)
		return nil
	}
	t.failed = false
	page := resp.ContactPage
	if before == "0" {
		t.newestPage(page, gen == strconv.FormatUint(t.gen, 10))
	} else {
		t.olderPage(page)
	}
	if t.stale && before == "0" {
		t.stale = false
		return m.refreshContact(contact, 0)
	}
	return nil
}

// stateRank orders a message's states so a merge only moves forward.
var stateRank = map[string]int{"queued": 0, "received": 0, "expired": 1, "refused": 1, "delivered": 2, "read": 3}

func (t *contactThread) merge(views []protocol.ContactMessageView) {
	for _, v := range views {
		old, held := t.rows[rowKey(v)]
		if held && stateRank[old.State] > stateRank[v.State] {
			v.State, v.DeliveredAt, v.ReadAt = old.State, old.DeliveredAt, old.ReadAt
		}
		if held && v.Text == "" {
			v.Text = old.Text // a collected text stays shown as it was read
		}
		t.rows[rowKey(v)] = v
	}
}

// newestPage merges the newest page. When it doesn't reach down to what
// the thread holds and more lies below it, the messages between can't be
// shown as complete: the thread restarts from this page, and paging older
// fetches the rest again, so no gap is ever drawn as continuous.
func (t *contactThread) newestPage(page *protocol.ContactMessagePage, current bool) {
	if !t.loaded || current {
		t.sync = page.Sync
		clear(t.refused) // a fresh page gives refused reads one more try
	}
	c := oldestCursor(page.Messages)
	if page.More && c != 0 && c > t.newestHeld() {
		clear(t.rows)
		t.offset = 0
	}
	t.merge(page.Messages)
	if t.boundaryFrom(current, c) {
		t.more, t.oldest = page.More, c
	}
	t.loaded = true
}

// boundaryFrom: a newest page sets the thread's older-page boundary when
// the thread has none yet, or when the page reaches as low as anything held.
// A thread already paged further down keeps its own.
func (t *contactThread) boundaryFrom(current bool, pageOldest int64) bool {
	if !t.loaded || t.oldest == 0 {
		return true
	}
	return current && pageOldest != 0 && pageOldest <= t.oldestHeld()
}

func (t *contactThread) newestHeld() int64 {
	var newest int64
	for _, v := range t.rows {
		newest = max(newest, v.Cursor)
	}
	return newest
}

func (t *contactThread) oldestHeld() int64 {
	return oldestCursor(slices.Collect(maps.Values(t.rows)))
}

// olderPage merges a page from below the boundary; an empty one means
// nothing older remains, whatever was there before was collected.
func (t *contactThread) olderPage(page *protocol.ContactMessagePage) {
	t.merge(page.Messages)
	c := oldestCursor(page.Messages)
	switch {
	case c == 0:
		t.more = false
	case t.oldest == 0 || c < t.oldest:
		t.oldest, t.more = c, page.More
	}
}

func oldestCursor(views []protocol.ContactMessageView) int64 {
	var oldest int64
	for _, v := range views {
		if oldest == 0 || v.Cursor < oldest {
			oldest = v.Cursor
		}
	}
	return oldest
}

// contactsChanged follows contact_messages_changed: a loaded thread reads
// its newest page again, one refresh at a time; an unloaded one loads when
// its tab opens.
func (m *Model) contactsChanged(contact string) tea.Cmd {
	t := m.contacts.threads[contact]
	if t == nil {
		return nil
	}
	if !t.loaded {
		t.failed = false // a failed first read may succeed now; the open tab loads it
		return nil
	}
	if m.requested(kindContactList, contact) {
		t.stale = true
		return nil
	}
	return m.refreshContact(contact, 0)
}

// contactSubmit sends the draft to the open contact tab's workspace. The
// operation's contact, ID and text are fixed now; a refusal keeps the draft.
func (m *Model) contactSubmit() tea.Cmd {
	contact := m.conv.selected.contact
	switch {
	case m.compose.draftEmpty():
		return nil
	case !m.contactsAvailable():
		m.status = "this broker doesn't carry contact messages; upgrade it"
		return nil
	case !m.confirmedContact(contact):
		m.status = contactConfirming
		return nil
	case !m.ready():
		m.status = "not connected; the draft is kept"
		return nil
	case m.contacts.send != nil:
		m.status = "the previous message to a workspace is still being sent"
		return nil
	}
	if m.guardSend() {
		return nil
	}
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend, ContactKey: contact,
		MessageID: uuid.NewString(), ContactText: m.compose.editor.Value()}
	m.contacts.send = &contactOp{msg: msg}
	m.status = "sending to the workspace…"
	return m.request(kindContactSend, msg, contact, msg.MessageID)
}

// contactSendResult settles the send: the draft clears only if it still
// holds the text sent, in the tab it was sent from.
func (m *Model) contactSendResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	op := m.contacts.send
	if op == nil || op.msg.MessageID != req.ids[1] {
		return nil // an answer to a send already settled
	}
	m.contacts.send = nil
	if resp.Type != protocol.TypeOperatorContactResult || resp.ContactMessage == nil {
		m.status = "not sent: " + refusalReason(resp)
		return nil
	}
	if m.conv.selected.contact == op.msg.ContactKey && m.compose.editor.Value() == op.msg.ContactText {
		m.compose.resetDraft()
	}
	t := m.contacts.thread(op.msg.ContactKey)
	t.merge([]protocol.ContactMessageView{*resp.ContactMessage})
	t.offset = 0
	m.status = "queued for the workspace; it leaves at the next sync"
	return nil
}

// contactsDisconnected leaves operations in flight unknown: their answers
// may be lost, so they are resent unchanged after the reconnect.
func (m *Model) contactsDisconnected() {
	if m.contacts.send != nil {
		m.contacts.send.unknown = true
	}
	for _, op := range m.contacts.reads {
		op.unknown = true
	}
	for _, t := range m.contacts.threads {
		t.stale, t.failed = false, false
	}
}

// contactsReconnected resends what was lost and reads loaded threads again:
// changes while offline were never announced here.
func (m *Model) contactsReconnected() tea.Cmd {
	if !m.contactsAvailable() {
		return nil
	}
	var cmds []tea.Cmd
	if op := m.contacts.send; op != nil && op.unknown {
		op.unknown = false
		m.status = "contact message outcome unknown; resending " + safe(op.msg.MessageID)
		cmds = append(cmds, m.request(kindContactSend, op.msg, op.msg.ContactKey, op.msg.MessageID))
	}
	for id, op := range m.contacts.reads {
		if op.unknown {
			op.unknown = false
			cmds = append(cmds, m.request(kindContactRead, op.msg, op.msg.ContactKey, id))
		}
	}
	for contact, t := range m.contacts.threads {
		if t.loaded {
			cmds = append(cmds, m.refreshContact(contact, 0))
		}
	}
	return tea.Batch(cmds...)
}
