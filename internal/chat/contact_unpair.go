package chat

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Unpairing a paired workspace (#276): /pair unpair in its ⇄ tab, or with
// its name or key, shows a card saying what ends. Only typing
// /pair unpair confirm and the first 8 digits of its key, once the card
// was drawn with the composer cleared, sends the one request, bound to
// the full key and the pairing epoch the card showed. A paste, an empty
// Enter, Esc, another tab, a reconnect or a change to that workspace in
// the pairing state discards the card unsent.

const (
	pairUnpair        = protocol.ContactOpUnpair
	panelUnpair       = "unpair"
	kindContactUnpair = "contact unpair"
	unpairConfirmWord = "confirm"
	unpairTokenLen    = 8
)

// unpairCard is the card on screen: the snapshot entry it binds, where
// it was opened and on which connection, and the operation ID its one
// request carries.
type unpairCard struct {
	contact protocol.PairingContactView
	view    viewKey
	gen     uint64
	id      string
	armed   bool // drawn with the composer cleared
}

// unpairState is the card, and the request it sent until answered.
type unpairState struct {
	card *unpairCard
	sent *contactOp
}

// unpairToken is what confirms a key's card: the first 8 digits of its
// digest.
func unpairToken(key string) string {
	digest := protocol.KeyDigest(key)
	return digest[:min(len(digest), unpairTokenLen)]
}

// unpairCommand is /pair unpair [NAME|KEY], or its confirmation.
func (m *Model) unpairCommand(fields pairWords) tea.Cmd {
	if !m.session.HasCapability(protocol.CapabilityContactUnpair) {
		m.status = "this broker can't unpair a workspace; upgrade it"
		return nil
	}
	if len(fields) == 3 && fields[1] == unpairConfirmWord {
		return m.unpairConfirm(fields[2])
	}
	c, problem := m.unpairTarget(fields[1:])
	if problem == "" {
		problem = unpairProblem(c, m.unpair.sent != nil)
	}
	if problem != "" {
		m.status = problem
		return nil
	}
	m.openUnpairCard(c)
	return nil
}

// unpairTarget is the workspace named, or the open ⇄ tab's.
func (m *Model) unpairTarget(named []string) (protocol.PairingContactView, string) {
	if len(named) > 0 {
		return m.namedContact(strings.Join(named, " "))
	}
	key := m.conv.selected
	if !key.isContact() {
		return protocol.PairingContactView{}, "open the workspace's ⇄ tab, or name it: /pair unpair NAME"
	}
	c, ok := m.contact(key.contact)
	if !ok {
		return c, "that workspace isn't in the pairing state; /pair status reads it"
	}
	return c, ""
}

// unpairProblem is why a contact's card can't open: it is unpaired
// already, has no pairing epoch listed, or an unpair is still in flight.
func unpairProblem(c protocol.PairingContactView, inFlight bool) string {
	switch {
	case unpairedHere(c):
		return "already unpaired: history kept; pairing again needs a fresh code"
	case c.PairingEpoch <= 0:
		return "the broker listed no pairing for it; /pair status, then /pair unpair again"
	case inFlight:
		return "the previous unpair is still being sent"
	}
	return ""
}

// openUnpairCard shows the card and clears the composer; it arms once
// drawn.
func (m *Model) openUnpairCard(c protocol.PairingContactView) {
	m.compose.resetDraft()
	m.unpair.card = &unpairCard{contact: c, view: m.conv.selected, gen: m.gen, id: uuid.NewString()}
	m.panel = &panel{kind: panelUnpair}
	m.status = "type /pair unpair confirm " + unpairToken(c.IdentityKey) + " to unpair, or Esc"
}

// unpairRows are the card, contract (f)'s words; drawing it with the
// composer cleared arms it.
func (m *Model) unpairRows() []string {
	card := m.unpair.card
	if card == nil {
		return []string{mutedStyle.Render("  no unpair card; /pair unpair opens one")}
	}
	if m.compose.draftEmpty() {
		card.armed = true
	}
	c := card.contact
	return []string{
		titleStyle.Render("Unpair "+safe(contactDisplay(c))+" ("+protocol.ShortKey(c.IdentityKey)+")?") + mutedStyle.Render("  (Esc keeps it)"),
		"  Revokes now: shares, review grants and consent end; queued messages expire; held requests end;",
		"  reviews running here finish without sending results.",
		"  History stays read-only. Pairing again needs a fresh code.",
		"  Type " + titleStyle.Render("/pair unpair confirm "+unpairToken(c.IdentityKey)) + ", or Esc.",
		mutedStyle.Render("  " + safe(c.IdentityKey) + " · pairing " + strconv.FormatInt(c.PairingEpoch, 10)),
	}
}

// unpairConfirm sends the armed card's one request, when the token is its
// key's.
func (m *Model) unpairConfirm(token string) tea.Cmd {
	card := m.unpair.card
	if problem := m.confirmProblem(card, token); problem != "" {
		m.status = problem
		return nil
	}
	c := card.contact
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: pairUnpair, ContactKey: c.IdentityKey,
		ContactConfirm: c.IdentityKey, ContactEpoch: c.PairingEpoch, MessageID: card.id}
	m.discardUnpairCard()
	m.unpair.sent = &contactOp{msg: msg}
	m.status = "unpairing " + safe(contactDisplay(c)) + "…"
	return m.request(kindContactUnpair, msg, c.IdentityKey, msg.MessageID)
}

// confirmProblem is why a confirmation sends nothing.
func (m *Model) confirmProblem(card *unpairCard, token string) string {
	switch {
	case card == nil:
		return "no unpair card is open; /pair unpair shows one"
	case !card.armed:
		return "the unpair card isn't shown yet; look at it, then confirm"
	case token != unpairToken(card.contact.IdentityKey):
		return "that isn't this card's key; type /pair unpair confirm " + unpairToken(card.contact.IdentityKey) + ", or Esc"
	case !m.ready():
		return "not connected; nothing sent"
	}
	return ""
}

// discardUnpairCard drops the card unsent and closes it.
func (m *Model) discardUnpairCard() {
	m.unpair.card = nil
	if m.panel != nil && m.panel.kind == panelUnpair {
		m.panel = nil
	}
}

// unpairInput discards an unsent card on a paste, Esc, an Enter outside
// the command line or a tab switch; handled is true when the key does
// nothing else.
func (m *Model) unpairInput(msg tea.Msg) bool {
	if m.unpair.card == nil {
		return false
	}
	if _, pasted := msg.(tea.PasteMsg); pasted {
		m.discardUnpairCard()
		return false
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return false
	}
	return m.unpairKey(key.String())
}

// cardKeys are the keys that discard a card: whether each does nothing
// else, and whether it does so only outside the command line, where Tab
// completes and Enter runs.
var cardKeys = map[string]struct{ swallow, outsideLine bool }{
	"esc":       {swallow: true},
	"enter":     {swallow: true, outsideLine: true},
	"tab":       {outsideLine: true},
	"shift+tab": {outsideLine: true},
}

func (m *Model) unpairKey(key string) bool {
	k, ok := cardKeys[key]
	if !ok || m.lineTakes(k.outsideLine) {
		return false
	}
	m.discardUnpairCard()
	m.status = "unpair discarded: nothing sent"
	return k.swallow
}

// lineTakes: the open command line takes a key that discards only
// outside it.
func (m *Model) lineTakes(outsideLine bool) bool {
	return outsideLine && m.compose.cmd != nil
}

// checkUnpairCard discards a card that no longer stands for what it
// showed: another connection, another tab, its panel replaced, or its
// workspace changed in the pairing state.
func (m *Model) checkUnpairCard() {
	card := m.unpair.card
	if card == nil || !m.cardStale(card) {
		return
	}
	m.discardUnpairCard()
	m.status = "unpair discarded: the pairing state changed; nothing sent"
}

func (m *Model) cardStale(card *unpairCard) bool {
	if card.gen != m.gen || card.view != m.conv.selected {
		return true
	}
	if m.panel == nil || m.panel.kind != panelUnpair {
		return true
	}
	c, ok := m.contact(card.contact.IdentityKey)
	return !ok || cardEntry(c) != cardEntry(card.contact)
}

// unpairEntry is what a card binds of its workspace: a change to any of
// it, its label included, discards the card.
type unpairEntry struct {
	key, label string
	epoch      int64
	unpaired   bool
}

func cardEntry(c protocol.PairingContactView) unpairEntry {
	return unpairEntry{key: c.IdentityKey, label: contactDisplay(c), epoch: c.PairingEpoch, unpaired: unpairedHere(c)}
}

// contactUnpairResult takes the snapshot the unpair answered with, so the
// tab turns read-only only once the broker did.
func (m *Model) contactUnpairResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	op := m.unpair.sent
	if op == nil || op.msg.MessageID != req.ids[1] {
		return nil
	}
	m.unpair.sent = nil
	if resp.Type != protocol.TypeOperatorContactResult || resp.ContactUnpaired == nil {
		m.status = "unpair refused: " + refusalReason(resp)
		return nil
	}
	if resp.PairingResult != nil {
		snapshot := *resp.PairingResult
		snapshot.Invitation = withoutCode(snapshot.Invitation)
		m.keepSnapshot(&snapshot)
		m.refreshDirectory()
	}
	m.status = "unpaired: history kept read-only; pairing again needs a fresh code"
	if !resp.ContactUnpaired.Changed {
		m.status = "already unpaired: nothing changed"
	}
	return nil
}

// unpairDisconnected leaves a sent unpair unknown, to resend unchanged.
func (m *Model) unpairDisconnected() {
	if m.unpair.sent != nil {
		m.unpair.sent.unknown = true
	}
}

// resendUnpair resends an unpair whose answer was lost: the same
// operation, key, confirmation and epoch, so a replay answers the first.
func (m *Model) resendUnpair() tea.Cmd {
	op := m.unpair.sent
	if op == nil || !op.unknown {
		return nil
	}
	op.unknown = false
	m.status = "unpair outcome unknown; resending " + safe(op.msg.MessageID)
	return m.request(kindContactUnpair, op.msg, op.msg.ContactKey, op.msg.MessageID)
}
