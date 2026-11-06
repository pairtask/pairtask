package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Paired mentions (#278): a message in a paired workspace's ⇄ conversation
// that tags this side's agents, or its operator, goes to that workspace as
// before and, in the same transaction, to them: one operator message, its
// replies assigned to the tagged agents, bound to the conversation. A
// tagged agent's reply comes back to that conversation and goes on to the
// same audience there, when the agent may still speak to it; whatever was
// decided is kept, so the same reply sent again is answered as it was.

const pairedSchema = `
CREATE TABLE IF NOT EXISTS paired_threads (
    local_id      TEXT PRIMARY KEY,
    contact       TEXT NOT NULL,
    root_local_id TEXT NOT NULL,
    peer_id       TEXT NOT NULL,
    peer_audience TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS paired_reply_outcomes (
    message_id    TEXT PRIMARY KEY,
    contact       TEXT NOT NULL,
    root_local_id TEXT NOT NULL,
    sender        TEXT NOT NULL,
    body_digest   TEXT NOT NULL,
    seq           INTEGER NOT NULL,
    peer          TEXT NOT NULL,
    reason        TEXT NOT NULL DEFAULT ''
);
`

// ErrConversationConflict is a reply in a shared conversation that names
// another destination, or the same reply ID sent again changed.
var ErrConversationConflict = errors.New("a reply in a shared conversation goes back to it unchanged; send anything else as a new message")

// ErrNotPaired: the message replied to belongs to no shared conversation.
var ErrNotPaired = errors.New("not a reply in a shared conversation")

// PairedBinding is the shared conversation a local message belongs to: its
// contact, the message replies attach to, that message's ID there, and who
// it went to there.
type PairedBinding struct {
	Contact, RootLocalID, PeerID string
	Audience                     protocol.PeerAudience
	epoch                        int64 // the pairing it was bound under (#276)
}

// PairedThread is the shared conversation a local message belongs to.
func (s *Store) PairedThread(ctx context.Context, id string) (PairedBinding, bool, error) {
	return pairedThread(ctx, s.db, id)
}

// KeepsConversations: this side keeps a shared conversation with contact,
// whether or not the contact is still there.
func (s *Store) KeepsConversations(ctx context.Context, contact string) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM paired_threads WHERE contact = ?)`, contact).Scan(&ok)
	return ok, err
}

func pairedThread(ctx context.Context, q rowQuerier, id string) (PairedBinding, bool, error) {
	var b PairedBinding
	var audience string
	err := q.QueryRowContext(ctx, `SELECT contact, root_local_id, peer_id, peer_audience, pairing_epoch FROM paired_threads WHERE local_id = ?`, id).
		Scan(&b.Contact, &b.RootLocalID, &b.PeerID, &audience, &b.epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return PairedBinding{}, false, nil
	}
	if err != nil {
		return PairedBinding{}, false, err
	}
	return b, true, json.Unmarshal([]byte(audience), &b.Audience)
}

// conversationRef is a delivery's conversation, as its frame names it.
func (b PairedBinding) conversationRef() *protocol.ConversationRef {
	return &protocol.ConversationRef{Kind: protocol.ConversationContact, Contact: b.Contact, Root: b.RootLocalID, PeerAudience: b.Audience}
}

// peerAudience is who a message to the contact goes to there.
func peerAudience(to []string) protocol.PeerAudience {
	if len(to) == 0 {
		return protocol.PeerAudience{Operator: true}
	}
	agents := slices.Clone(to)
	slices.Sort(agents)
	return protocol.PeerAudience{Agents: slices.Compact(agents)}
}

// homeTargets are the send's tagged agents here, and its operator when
// tagged, in one canonical list.
func (c ContactSend) homeTargets() []string {
	targets := slices.Clone(c.Here)
	if c.OperatorHere {
		targets = append(targets, protocol.OperatorID)
	}
	slices.Sort(targets)
	return slices.Compact(targets)
}

// homeRecipients are the targets as persisted: each tagged agent with the
// session the hub resolved it to, if any, and this side's operator.
func (c ContactSend) homeRecipients(targets []string) []protocol.Recipient {
	recipients := make([]protocol.Recipient, len(targets))
	for i, id := range targets {
		recipients[i] = protocol.Recipient{AgentID: id}
		if j := slices.IndexFunc(c.Recipients, func(r protocol.Recipient) bool { return r.AgentID == id }); j >= 0 {
			recipients[i] = c.Recipients[j]
		}
	}
	return recipients
}

// shareHere delivers a ⇄ send to the agents and operator it tags here, in
// the send's transaction: one operator message, the tagged agents' replies
// assigned to them, bound to the conversation.
func shareHere(ctx context.Context, tx *sql.Tx, c ContactSend, peer messaging.Message) error {
	targets := c.homeTargets()
	if len(targets) == 0 {
		return nil
	}
	seq, err := messageSlot(ctx, tx, c.Seq)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"text": c.Text}) // a map of strings always encodes
	ts := c.Now.UTC()
	msg := protocol.Message{Type: protocol.TypeMsg, Seq: seq, TS: &ts, From: protocol.OperatorID, To: "group:" + c.OperationID,
		Origin: protocol.OriginOperator, Payload: payload, MessageID: c.OperationID, AckRequested: true, Durable: true, Group: targets}
	if err := persistMessage(ctx, tx, routedMessage{msg: msg, recipients: c.homeRecipients(targets)}); err != nil {
		return err
	}
	if err := insertAssignments(ctx, tx, OperatorMessage{Msg: msg, Assignees: c.Here}); err != nil {
		return err
	}
	return bindThread(ctx, tx, threadRow{local: msg.MessageID, contact: c.Contact, root: msg.MessageID, peer: peer.ID, audience: peerAudience(peer.To)})
}

// messageSlot is the order a message takes: the one given, or the next.
func messageSlot(ctx context.Context, tx *sql.Tx, seq uint64) (uint64, error) {
	if seq != 0 {
		return seq, nil
	}
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM messages`).Scan(&seq)
	return seq, err
}

// threadRow binds a local message to its shared conversation.
type threadRow struct {
	local, contact, root, peer string
	audience                   protocol.PeerAudience
}

// bindThread binds it under the contact's current pairing (#276).
func bindThread(ctx context.Context, tx *sql.Tx, r threadRow) error {
	audience, _ := json.Marshal(r.audience) // a struct of a bool and strings always encodes
	_, err := tx.ExecContext(ctx, `INSERT INTO paired_threads (local_id, contact, root_local_id, peer_id, peer_audience, pairing_epoch)
VALUES (?2, ?1, ?3, ?4, ?5, `+currentEpochOf1+`)`, r.contact, r.local, r.root, r.peer, string(audience))
	return err
}

// withConversations names the shared conversation of each replayed message
// that has one (#278).
func withConversations(ctx context.Context, tx *sql.Tx, messages []protocol.Message) error {
	for i := range messages {
		b, found, err := pairedThread(ctx, tx, messages[i].MessageID)
		if err != nil {
			return err
		}
		if found {
			messages[i].Conversation = b.conversationRef()
		}
	}
	return nil
}

// PairedReply is a tagged agent's reply in a shared conversation: the frame
// as sent, the sender's session, when, and its order (0 for the next).
type PairedReply struct {
	Msg     protocol.Message
	Session string
	Now     time.Time
	Seq     uint64
}

// PairedReplyResult is what became of it: where it is here, whether it was
// the same reply again, and whether it went on there.
type PairedReplyResult struct {
	Seq          uint64
	Repeated     bool
	PeerDelivery protocol.PeerDelivery
	Binding      PairedBinding
}

// PairedReply keeps a reply in its shared conversation and, when the agent
// may still speak to that workspace, queues it there to the same audience,
// in one transaction. The same reply again is answered as it was; one that
// names another destination, or changed, is a conflict. A conversation
// bound under a pairing since ended takes no reply: ErrUnpaired (#276).
func (s *Store) PairedReply(ctx context.Context, r PairedReply) (PairedReplyResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PairedReplyResult{}, err
	}
	defer tx.Rollback()
	b, err := replyBinding(ctx, tx, r.Msg)
	if err != nil {
		return PairedReplyResult{}, err
	}
	reply := pairedReply{r, b, replyDigest(r.Msg, b.RootLocalID)}
	if held, ok, err := reply.replayed(ctx, tx); err != nil || ok {
		return held, err
	}
	result, err := reply.keep(ctx, tx)
	if err != nil {
		return PairedReplyResult{}, err
	}
	return result, tx.Commit()
}

// replyBinding is the conversation a reply answers, under a pairing that
// hasn't ended, when the reply names it.
func replyBinding(ctx context.Context, tx *sql.Tx, m protocol.Message) (PairedBinding, error) {
	b, found, err := pairedThread(ctx, tx, m.InReplyTo)
	if err != nil || !found {
		return PairedBinding{}, errors.Join(err, ErrNotPaired)
	}
	if elsewhere(m, b) {
		return PairedBinding{}, ErrConversationConflict
	}
	return b, refuseRetired(ctx, tx, pairingKey{b.Contact, b.epoch})
}

// elsewhere: the reply names another destination than its conversation.
func elsewhere(m protocol.Message, b PairedBinding) bool {
	switch {
	case m.To == protocol.Broadcast || m.Room != "":
		return true
	case m.Workspace == "":
		return m.To != protocol.OperatorID
	case m.Workspace != b.Contact:
		return true
	}
	return m.To != protocol.OperatorID && !slices.Contains(b.Audience.Agents, m.To)
}

// replyDigest is what the reply says, from whom, under which conversation.
func replyDigest(m protocol.Message, root string) string {
	sum := sha256.Sum256([]byte(m.From + "\x00" + root + "\x00" + string(compactPayload(m.Payload))))
	return hex.EncodeToString(sum[:])
}

// pairedReply is a reply being kept.
type pairedReply struct {
	PairedReply
	binding PairedBinding
	digest  string
}

// replayed is the reply's earlier result, when the same reply was kept
// before; another reply under its ID is a conflict.
func (p pairedReply) replayed(ctx context.Context, tx *sql.Tx) (PairedReplyResult, bool, error) {
	var sender, digest, peer, reason string
	var seq uint64
	err := tx.QueryRowContext(ctx, `SELECT sender, body_digest, seq, peer, reason FROM paired_reply_outcomes WHERE message_id = ?`,
		p.Msg.MessageID).Scan(&sender, &digest, &seq, &peer, &reason)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return PairedReplyResult{}, false, nil
	case err != nil:
		return PairedReplyResult{}, false, err
	case sender != p.Msg.From || digest != p.digest:
		return PairedReplyResult{}, false, ErrConversationConflict
	}
	return PairedReplyResult{Seq: seq, Repeated: true, PeerDelivery: protocol.PeerDelivery{State: peer, Reason: reason}, Binding: p.binding}, true, nil
}

// keep writes the reply here, its copy there when it may go, and what was
// decided.
func (p pairedReply) keep(ctx context.Context, tx *sql.Tx) (PairedReplyResult, error) {
	delivery, err := p.mayGo(ctx, tx)
	if err != nil {
		return PairedReplyResult{}, err
	}
	seq, err := p.keepHere(ctx, tx)
	if err != nil {
		return PairedReplyResult{}, err
	}
	if err := p.keepThere(ctx, tx, delivery); err != nil {
		return PairedReplyResult{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO paired_reply_outcomes
(message_id, contact, root_local_id, sender, body_digest, seq, peer, reason, pairing_epoch)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.Msg.MessageID, p.binding.Contact, p.binding.RootLocalID, p.Msg.From, p.digest, seq,
		delivery.State, delivery.Reason, p.binding.epoch)
	return PairedReplyResult{Seq: seq, PeerDelivery: delivery, Binding: p.binding}, err
}

// mayGo is whether the reply goes on there: never to a contact removed or
// paused, and only from an agent shared with it or holding this
// conversation's reply as its operator assigned it.
func (p pairedReply) mayGo(ctx context.Context, tx *sql.Tx) (protocol.PeerDelivery, error) {
	switch err := requireActive(ctx, tx, p.binding.Contact); {
	case errors.Is(err, messaging.ErrUnknownContact):
		return notSent(protocol.PeerContactRemoved), nil
	case errors.Is(err, ErrPaused):
		return notSent(protocol.PeerPaused), nil
	case err != nil:
		return protocol.PeerDelivery{}, err
	}
	allowed, err := p.speaksThere(ctx, tx)
	if err != nil || !allowed {
		return notSent(protocol.PeerNotShared), err
	}
	return protocol.PeerDelivery{State: protocol.PeerQueued}, nil
}

func notSent(reason string) protocol.PeerDelivery {
	return protocol.PeerDelivery{State: protocol.PeerNotSent, Reason: reason}
}

// speaksThere: the agent is shared with the contact, or holds the reply its
// operator assigned it in this conversation.
func (p pairedReply) speaksThere(ctx context.Context, tx *sql.Tx) (bool, error) {
	var ok bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM peer_grants WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` AND agent = ?2)
    OR EXISTS (SELECT 1 FROM assignments WHERE operator_message_id = ?3 AND agent_id = ?2 AND task = ?4 AND state = ?5)`,
		p.binding.Contact, p.Msg.From, p.binding.RootLocalID, protocol.TaskReply, protocol.AssignmentAssigned).Scan(&ok)
	return ok, err
}

// keepHere keeps the reply in the conversation here, answering its root,
// for this side's operator.
func (p pairedReply) keepHere(ctx context.Context, tx *sql.Tx) (uint64, error) {
	seq, err := messageSlot(ctx, tx, p.Seq)
	if err != nil {
		return 0, err
	}
	ts := p.Now.UTC()
	msg := protocol.Message{Type: protocol.TypeMsg, Seq: seq, TS: &ts, From: p.Msg.From, To: protocol.OperatorID, Origin: protocol.OriginAgent,
		Payload: p.Msg.Payload, MessageID: p.Msg.MessageID, InReplyTo: p.binding.RootLocalID, AckRequested: true, Durable: true}
	err = persistMessage(ctx, tx, routedMessage{msg: msg, senderSession: p.Session, recipients: []protocol.Recipient{{AgentID: protocol.OperatorID}}})
	if err != nil {
		return 0, err
	}
	return seq, bindThread(ctx, tx, threadRow{local: msg.MessageID, contact: p.binding.Contact, root: p.binding.RootLocalID,
		peer: p.binding.PeerID, audience: p.binding.Audience})
}

// keepThere puts the reply in that workspace's history here: queued to the
// conversation's audience there when it may go, else kept here only, with
// why it didn't go.
func (p pairedReply) keepThere(ctx context.Context, tx *sql.Tx, delivery protocol.PeerDelivery) error {
	m := p.peerCopy()
	if !validOutgoing(m) {
		return messaging.ErrInvalid
	}
	if delivery.State == protocol.PeerQueued {
		_, err := queueIn(ctx, tx, outgoing{key: messageKey{p.binding.Contact, messaging.DirOut, m.ID}, m: m, now: p.Now, localID: m.ID, assigned: true})
		return err
	}
	return keepNotSent(ctx, tx, keptRow{contact: p.binding.Contact, m: m, now: p.Now, reason: delivery.Reason})
}

// peerCopy is the reply as that workspace would get it, answering the
// message the root went there as.
func (p pairedReply) peerCopy() messaging.Message {
	var text struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(p.Msg.Payload, &text) // the hub checked the payload
	return messaging.Message{ID: p.Msg.MessageID, Text: text.Text, From: p.Msg.From, To: p.binding.Audience.Agents,
		ReplyTo: messaging.ReplyRef{ID: p.binding.PeerID, Author: messaging.ReplyBySender}}
}

// keptRow is a reply kept in a contact's history here only, and why.
type keptRow struct {
	contact string
	m       messaging.Message
	now     time.Time
	reason  string
}

// keepNotSent stores a reply that didn't go there as a DirKept row: read in
// the contact's history like any other, never offered to it, and kept even
// once the contact is gone. It numbers in the contact's current pairing.
func keepNotSent(ctx context.Context, tx *sql.Tx, k keptRow) error {
	var n int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(n), 0) + 1 FROM peer_messages WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+`
    AND direction = ?2`, k.contact, messaging.DirKept).Scan(&n)
	if err != nil {
		return err
	}
	m := k.m
	m.N, m.CreatedAt = uint64(n), k.now.UTC()
	hash := m.Hash()
	_, err = tx.ExecContext(ctx, `INSERT INTO peer_messages
(contact, pairing_epoch, direction, message_id, n, created_at, text, text_bytes, text_hash, hash, state, reason, stored_at,
    from_agent, to_agents, reply_id, reply_author, local_id)
VALUES (?, `+currentEpoch+`, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		k.contact, k.contact, messaging.DirKept, m.ID, n, msgStamp(m.CreatedAt), m.Text, len(m.Text), textHash(m.Text), hash[:],
		messaging.StateNotSent, k.reason, msgStamp(k.now), nullString(m.From), nullList(m.To), nullString(m.ReplyTo.ID), replyAuthor(m), m.ID)
	return err
}
