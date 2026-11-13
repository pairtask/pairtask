package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// peerMessagesColumns keeps messages between paired workspaces (#210),
// both directions, one row each. A message's ID, sequence, time and hash
// never change; its text is collected within the bounds, and an inbound row
// is the tombstone that stops a replay until the floor passes it.
// The transition creates it, or rebuilds a released one, with
// pairing_epoch in its key (#276).
const peerMessagesColumns = `(
    cursor       INTEGER PRIMARY KEY AUTOINCREMENT,
    contact      TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL DEFAULT 1,
    direction    TEXT NOT NULL,
    message_id   TEXT NOT NULL,
    n            INTEGER NOT NULL,
    created_at   TEXT NOT NULL,
    text         TEXT,
    text_bytes   INTEGER NOT NULL,
    text_hash    BLOB NOT NULL,
    hash         BLOB NOT NULL,
    state        TEXT NOT NULL,
    class        TEXT NOT NULL DEFAULT '',
    reason       TEXT NOT NULL DEFAULT '',
    stored_at    TEXT NOT NULL,
    delivered_at TEXT,
    read_at      TEXT,
    receipt      TEXT NOT NULL DEFAULT '',
    receipt_at   TEXT,
    UNIQUE (contact, pairing_epoch, direction, message_id),
    UNIQUE (contact, pairing_epoch, direction, n)
)`

// peerMessageSequencesColumns is peer_message_sequences with pairing_epoch in its key (#276).
const peerMessageSequencesColumns = `(
    contact TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL DEFAULT 1,
    next    INTEGER NOT NULL,
    PRIMARY KEY (contact, pairing_epoch)
)`

// peerMessageFloorsColumns is peer_message_floors with pairing_epoch in its key (#276).
const peerMessageFloorsColumns = `(
    contact TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL DEFAULT 1,
    floor   INTEGER NOT NULL,
    PRIMARY KEY (contact, pairing_epoch)
)`

// messagePageLimit bounds one Page.
const messagePageLimit = 64

// PeerMessages is the store's messages between paired workspaces.
type PeerMessages struct {
	db    *sql.DB
	fence *linkFence   // nil for the root store; set by Admit (#276)
	gate  *consentGate // the store's session admission gate (#293)
}

var _ messaging.Store = (*PeerMessages)(nil)

// PeerMessages is the messaging store over this database.
func (s *Store) PeerMessages() *PeerMessages { return &PeerMessages{db: s.db, gate: s.gate} }

// confirmedContact: a contact row whose pairing wasn't ended since (#276)
// and whose pairing is confirmed: one installed through the exchange
// ledger by the installed row its pairing_op names, nothing else; a legacy
// one by its decision, as the inviter (a delivered acceptance) or as the
// joiner (a confirmed contact join). A pending or rejected pairing never
// counts.
func confirmedContact(ctx context.Context, q rowQuerier, contact contactKey) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM peer_contacts c WHERE c.identity_key = ?1 AND c.unpaired_at = '' AND `+pairingConfirmed+`)`,
		string(contact)).Scan(&ok)
	return ok, err
}

// knownContact: a confirmed contact, or one whose pairing was confirmed and
// has ended since; an unpaired contact keeps its history (#276).
func knownContact(ctx context.Context, q rowQuerier, contact contactKey) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM peer_contacts c WHERE c.identity_key = ?1 AND (c.unpaired_at != '' OR `+pairingConfirmed+`))`,
		string(contact)).Scan(&ok)
	return ok, err
}

// pairingConfirmed is the confirmed pairing of contact row c, identity ?1:
// a pairing ID is confirmed only by the installed row its pairing_op
// names; a legacy pairing, with none, by its decision.
const pairingConfirmed = `(
    (c.pairing_id IS NOT NULL AND EXISTS (SELECT 1 FROM pairing_exchanges x
        WHERE x.operation_id = c.pairing_op AND x.identity_key = ?1 AND x.state = 'installed'))
    OR (c.pairing_id IS NULL AND ` + pairingDecided + `))`

// pairingDecided is the legacy confirmed decision for identity ?1.
const pairingDecided = `(
    EXISTS (SELECT 1 FROM peer_pairing_requests WHERE identity_key = ?1 AND state = 'accepted' AND delivered_at IS NOT NULL)
    OR EXISTS (SELECT 1 FROM peer_joins WHERE peer = ?1 AND state = 'contact' AND decision_confirmed_at IS NOT NULL))`

// contactKey is a contact's identity key.
type contactKey string

// requireContact: a confirmed contact; an unpaired one is ErrUnpaired.
func requireContact(ctx context.Context, q rowQuerier, contact contactKey) error {
	if err := requireKnown(ctx, q, contact); err != nil {
		return err
	}
	return refuseUnpaired(ctx, q, string(contact))
}

// requireKnown: a confirmed contact, or an unpaired one.
func requireKnown(ctx context.Context, q rowQuerier, contact contactKey) error {
	ok, err := knownContact(ctx, q, contact)
	if err == nil && !ok {
		err = messaging.ErrUnknownContact
	}
	return err
}

func textHash(text string) []byte {
	sum := sha256.Sum256([]byte(text))
	return sum[:]
}

// Queue stores an outbound message with the contact's next sequence. A
// message from an agent needs the agent shared with the contact.
func (p *PeerMessages) Queue(ctx context.Context, o messaging.Outgoing) (messaging.Row, error) {
	if err := p.localOnly(); err != nil {
		return messaging.Row{}, err
	}
	m := messaging.Message{ID: o.ID, Text: o.Text, From: o.From, To: o.To, ReplyTo: o.ReplyTo, Review: o.Review, Seal: o.Seal}
	if !validOutgoing(m) {
		return messaging.Row{}, fmt.Errorf("%w: ID, text or routing", messaging.ErrInvalid)
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return messaging.Row{}, err
	}
	defer tx.Rollback()
	key := messageKey{o.Contact, messaging.DirOut, o.ID}
	row, err := queueIn(ctx, tx, outgoing{key: key, m: m, now: o.Now, localID: o.LocalID})
	if err != nil {
		return messaging.Row{}, err
	}
	if err := tx.Commit(); err != nil {
		return messaging.Row{}, err
	}
	return row, nil
}

// validOutgoing: a message's ID, text and routing are within the rules.
func validOutgoing(m messaging.Message) bool {
	if !messaging.ValidID(m.ID) || !messaging.ValidText(m.Text) {
		return false
	}
	return messaging.ValidRouting(m) && validAsk(m)
}

// validAsk: no review request, or one within the rules.
func validAsk(m messaging.Message) bool {
	return m.Review == nil || messaging.ValidReviewAsk(m)
}

// outgoing is one message the operator or an agent queues: its ID, text
// and routing, without its sequence or time yet.
type outgoing struct {
	key     messageKey
	m       messaging.Message
	now     time.Time
	localID string
	// assigned: an agent's reply its operator assigned it in a shared
	// conversation (#278), which the caller found it holds; it speaks
	// there without being shared.
	assigned bool
	// supersedes is the sender's earlier request a review request replaces
	// (#259), kept on this side only; "" for none.
	supersedes string
	// sealer seals a new message that carries no seal of its own (#115).
	sealer PeerSealer
}

// queueIn is Queue inside its transaction: the existing row for a replay,
// or a new one with the next sequence; either keeps the message's seal.
func queueIn(ctx context.Context, tx *sql.Tx, o outgoing) (messaging.Row, error) {
	row, err := queueRow(ctx, tx, o)
	if err != nil {
		return row, err
	}
	if err := keepSeal(ctx, tx, o.key, o.m.Seal); err != nil {
		return row, err
	}
	rows := []messaging.Row{row}
	err = withSeals(ctx, tx, rows)
	return rows[0], err
}

// queueRow finds or queues the message's row.
func queueRow(ctx context.Context, tx *sql.Tx, o outgoing) (messaging.Row, error) {
	key := o.key
	if err := requireContact(ctx, tx, contactKey(key.contact)); err != nil {
		return messaging.Row{}, err
	}
	row, found, err := messageRow(ctx, tx, key)
	if err != nil || found {
		return o.replayedAsk(ctx, tx, row, err)
	}
	// A paused contact takes nothing new; a retry above still answers (#276).
	if err := requireActive(ctx, tx, key.contact); err != nil {
		return messaging.Row{}, err
	}
	if err := refuseUnpairedByThem(ctx, tx, key.contact); err != nil {
		return messaging.Row{}, err
	}
	if err := o.senderMaySpeak(ctx, tx); err != nil {
		return messaging.Row{}, err
	}
	if err := outboxRoom(ctx, tx, o); err != nil {
		return messaging.Row{}, err
	}
	return queueNew(ctx, tx, o)
}

// queueNew queues a new message with the next sequence, and records the
// review request it is, if it is one.
func queueNew(ctx context.Context, tx *sql.Tx, o outgoing) (messaging.Row, error) {
	n, err := nextSequence(ctx, tx, contactKey(o.key.contact))
	if err != nil {
		return messaging.Row{}, err
	}
	m := o.m
	m.N, m.CreatedAt = n, o.now.UTC()
	if len(m.Seal) == 0 && o.sealer != nil {
		m.Seal = o.sealer(o.key.contact, m)
	}
	if err := insertMessage(ctx, tx, newRow{key: o.key, m: m, now: o.now, localID: o.localID}); err != nil {
		return messaging.Row{}, err
	}
	if err := keepSeal(ctx, tx, o.key, m.Seal); err != nil {
		return messaging.Row{}, err
	}
	if err := recordedAsk(ctx, tx, o); err != nil {
		return messaging.Row{}, err
	}
	row, _, err := messageRow(ctx, tx, o.key)
	return row.Row, err
}

// senderMaySpeak: the operator, an agent shared with the contact, or one
// replying as its operator assigned it.
func (o outgoing) senderMaySpeak(ctx context.Context, tx *sql.Tx) error {
	if o.assigned {
		return nil
	}
	return senderShared(ctx, tx, o.key.contact, o.m.From)
}

// senderShared: the operator, or an agent shared with the contact.
func senderShared(ctx context.Context, tx *sql.Tx, contact, from string) error {
	if from == "" {
		return nil
	}
	ok, err := allGranted(ctx, tx, contact, []string{from})
	if err == nil && !ok {
		err = messaging.ErrNotShared
	}
	return err
}

// replayedAsk is replayed, for a review request also naming the same
// request it replaces as first (#259).
func (o outgoing) replayedAsk(ctx context.Context, tx *sql.Tx, row storedRow, err error) (messaging.Row, error) {
	held, err := replayed(row, o, err)
	if err != nil {
		return held, err
	}
	return held, sameSupersedes(ctx, tx, o)
}

// replayed answers a Queue of an ID already queued: the same text and
// routing is the same message, anything else a conflict.
func replayed(row storedRow, o outgoing, err error) (messaging.Row, error) {
	switch {
	case err != nil:
		return messaging.Row{}, err
	case string(textHash(o.m.Text)) != string(row.textHash) || !sameRouting(row.Message, o.m):
		return messaging.Row{}, messaging.ErrConflict
	}
	return row.Row, nil
}

// sameRouting: the same sender, recipients in order, and reply.
func sameRouting(a, b messaging.Message) bool {
	if a.From != b.From || a.ReplyTo != b.ReplyTo {
		return false
	}
	return slices.Equal(a.To, b.To) && sameAsk(a.Review, b.Review)
}

// sameAsk: both no review request, or the very same one.
func sameAsk(a, b *messaging.ReviewAsk) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// outboxRoom refuses a message the contact's queued messages, in its
// current pairing, leave no room for.
func outboxRoom(ctx context.Context, tx *sql.Tx, o outgoing) error {
	var held budget
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(text_bytes), 0) FROM peer_messages
WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` AND direction = 'out' AND state = 'queued'`, o.key.contact).Scan(&held.count, &held.bytes)
	if err != nil {
		return err
	}
	if !outboxBudget.holds(budget{held.count, held.bytes + len(o.m.Text)}) {
		return messaging.ErrOutboxFull
	}
	return nil
}

// budget is a bound on messages and on their bytes.
type budget struct{ count, bytes int }

var (
	outboxBudget    = budget{messaging.OutboxMessages, messaging.OutboxBytes}
	unreadBudget    = budget{messaging.UnreadMessages, messaging.UnreadBytes}
	retentionBudget = budget{messaging.RetainedText, messaging.RetainedTextBytes}
)

// holds: a count below the bound and bytes within it.
func (b budget) holds(used budget) bool {
	return used.count < b.count && used.bytes <= b.bytes
}

// clamp is a caller's budget kept within this one, never negative.
func (b budget) clamp(asked budget) budget {
	return budget{max(0, min(asked.count, b.count)), max(0, min(asked.bytes, b.bytes))}
}

// sessionBudget is what one session may carry each way.
var sessionBudget = budget{messaging.SessionMessages, messaging.SessionBytes}

// nextSequence takes the contact's next sequence, from 1; it is never
// reused and never wraps.
func nextSequence(ctx context.Context, tx *sql.Tx, contact contactKey) (uint64, error) {
	var next int64 = 1
	err := tx.QueryRowContext(ctx, `SELECT next FROM peer_message_sequences WHERE contact = ? AND pairing_epoch = `+currentEpoch, string(contact), string(contact)).Scan(&next)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if next < 1 || next == math.MaxInt64 {
		return 0, errors.New("messaging: sequence exhausted for this contact")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO peer_message_sequences (contact, pairing_epoch, next) VALUES (?, `+currentEpoch+`, ?)
ON CONFLICT(contact, pairing_epoch) DO UPDATE SET next = excluded.next`, string(contact), string(contact), next+1)
	return uint64(next), err
}

// initialState is a new row's state by direction.
var initialState = map[string]string{messaging.DirOut: messaging.StateQueued, messaging.DirIn: messaging.StateReceived}

// newRow is a message to insert: its key, the message and when, and for
// an outbound one queued from a local agent's message, that message.
type newRow struct {
	key     messageKey
	m       messaging.Message
	now     time.Time
	localID string
	// consented: this side's operator allowed the request it was held as
	// (#293); a review request it stages is approved.
	consented bool
}

// state is a new row's: inbound for this side's agents is routed, never a
// human's to read.
func (r newRow) state() string {
	if r.key.direction == messaging.DirIn && len(r.m.To) > 0 {
		return messaging.StateRouted
	}
	return initialState[r.key.direction]
}

// insertMessage stores a message in its contact's current pairing: an ID
// an earlier pairing used is a new message there, the old row history.
func insertMessage(ctx context.Context, tx *sql.Tx, r newRow) error {
	key, m, now := r.key, r.m, r.now
	hash := m.Hash()
	_, err := tx.ExecContext(ctx, `INSERT INTO peer_messages
(contact, pairing_epoch, direction, message_id, n, created_at, text, text_bytes, text_hash, hash, state, stored_at,
    from_agent, to_agents, reply_id, reply_author, local_id, review_ask)
VALUES (?, `+currentEpoch+`, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key.contact, key.contact, key.direction, m.ID, int64(m.N), msgStamp(m.CreatedAt), m.Text, len(m.Text), textHash(m.Text), hash[:],
		r.state(), msgStamp(now), nullString(m.From), nullList(m.To), nullString(m.ReplyTo.ID), replyAuthor(m), nullString(r.localID), reviewAskJSON(m.Review))
	return err
}

// replyAuthor is the side a reply's message is on, or NULL for none.
func replyAuthor(m messaging.Message) sql.NullInt64 {
	if m.ReplyTo.Author == messaging.ReplyAuthorNone {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(m.ReplyTo.Author), Valid: true}
}

// holdForUpgrade says why queued messages a session's peer can't carry
// stay queued; a delivery clears it. Only the current pairing's are offered.
func holdForUpgrade(ctx context.Context, tx *sql.Tx, contact, carried string) error {
	if carried == carriedAll {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE peer_messages SET class = ?2, reason = ?3
WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` AND direction = 'out' AND state = 'queued' AND NOT (`+carried+`)`,
		contact, messaging.Transient, messaging.ReasonPeerUpgrade)
	return err
}

// What a session carries, by its version: every row; no review requests
// before ReviewVersion; nothing to or from agents before RoutingVersion.
const (
	carriedAll  = "1"
	noReviewAsk = `review_ask IS NULL`
	unroutedRow = `from_agent IS NULL AND to_agents IS NULL AND reply_id IS NULL AND ` + noReviewAsk
)

// carriedAt is the rows a session at version carries; 0 is the newest.
func carriedAt(version byte) string {
	switch {
	case version == 0 || version >= messaging.ReviewVersion:
		return carriedAll
	case version >= messaging.RoutingVersion:
		return noReviewAsk
	}
	return unroutedRow
}

// Offer is the oldest queued messages, in the contact's current pairing
// (#276), that fit the session bounds; queued
// messages past QueuedExpiry are expired first and never offered.
// Before RoutingVersion, messages to or from agents stay queued.
func (p *PeerMessages) Offer(ctx context.Context, contact string, now time.Time, l messaging.Limit) ([]messaging.Row, error) {
	carried := carriedAt(l.Version)
	limit := sessionBudget.clamp(budget{l.Count, l.Bytes})
	var out []messaging.Row
	err := p.inTx(ctx, contact, func(tx *sql.Tx) error {
		if err := expireQueued(ctx, tx, contact, now); err != nil {
			return err
		}
		if err := holdForUpgrade(ctx, tx, contact, carried); err != nil {
			return err
		}
		rows, err := queryRows(ctx, tx, `WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+`
    AND direction = 'out' AND state = 'queued' AND `+carried+` ORDER BY n`, contact)
		if err != nil {
			return err
		}
		out = within(rows, budget{limit.count + 1, limit.bytes})
		return withSeals(ctx, tx, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// within is the leading rows whose count and text the budget holds.
func within(rows []storedRow, b budget) []messaging.Row {
	var out []messaging.Row
	bytes := 0
	for _, r := range rows {
		bytes += len(r.Text)
		if !b.holds(budget{len(out) + 1, bytes}) {
			break
		}
		out = append(out, r.Row)
	}
	return out
}

// expireQueued expires the current pairing's queued messages past
// QueuedExpiry; an earlier pairing's are history.
func expireQueued(ctx context.Context, tx *sql.Tx, contact string, now time.Time) error {
	ids, err := queryStrings(ctx, tx, `UPDATE peer_messages SET state = 'expired'
WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` AND direction = 'out' AND state = 'queued' AND created_at < ?2
RETURNING message_id`, contact, msgStamp(now.Add(-messaging.QueuedExpiry)))
	if err != nil {
		return err
	}
	for _, id := range ids {
		// A review request never sent ends unsent.
		if err := settleAsks(ctx, tx, askEnd{"unsent", "", now}, currentAsk(contact, id)); err != nil {
			return err
		}
	}
	return nil
}

// Delivered records the peer's Stored: a queued or expired message is
// delivered; a read one stays read, and the time is kept once set. The ID
// names a message of the contact's current pairing: one only an earlier
// pairing used is unknown here (#276).
func (p *PeerMessages) Delivered(ctx context.Context, contact, id string, at time.Time) error {
	return p.exec(ctx, contact, `UPDATE peer_messages SET
    delivered_at = COALESCE(delivered_at, ?3),
    state = CASE WHEN state IN ('queued', 'expired') THEN 'delivered' ELSE state END,
    class = CASE WHEN state IN ('queued', 'expired') THEN '' ELSE class END,
    reason = CASE WHEN state IN ('queued', 'expired') THEN '' ELSE reason END
WHERE `+currentOutbound+` AND state IN ('queued', 'expired', 'delivered', 'read')`,
		contact, id, msgStamp(at))
}

// currentOutbound is this side's message ?2 to contact ?1 in its current
// pairing: the peer's answers name only those (#276).
const currentOutbound = `contact = ?1 AND pairing_epoch = ` + currentEpochOf1 + ` AND direction = 'out' AND message_id = ?2`

// exec runs one statement for contact in a fenced transaction.
func (p *PeerMessages) exec(ctx context.Context, contact, query string, args ...any) error {
	return p.inTx(ctx, contact, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
}

// Refused records a refusal of a queued message: a terminal one ends it, a
// transient one keeps it queued with the reason.
func (p *PeerMessages) Refused(ctx context.Context, contact string, refused messaging.Refused) error {
	id, r := refused.ID, refused.Refusal
	if !r.Valid() {
		return fmt.Errorf("%w: refusal", messaging.ErrInvalid)
	}
	state := messaging.StateQueued
	if r.Class == messaging.Terminal {
		state = messaging.StateRefused
	}
	return p.inTx(ctx, contact, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE peer_messages SET state = ?3, class = ?4, reason = ?5
WHERE `+currentOutbound+` AND state = 'queued'`, contact, id, state, r.Class, r.Reason); err != nil {
			return err
		}
		if state != messaging.StateRefused {
			return nil
		}
		return settleCurrentRefusal(ctx, tx, contact, refused)
	})
}

// settleCurrentRefusal ends a review request refused for good, and tells the
// agent that sent it (#302); a held one waits. An ID the contact's current
// pairing doesn't hold refuses nothing (#276).
func settleCurrentRefusal(ctx context.Context, tx *sql.Tx, contact string, refused messaging.Refused) error {
	_, found, err := messageRow(ctx, tx, messageKey{contact, messaging.DirOut, refused.ID})
	if err != nil || !found {
		return err
	}
	return settleRefusedAsk(ctx, tx, contact, refused)
}

// PeerRead records the peer's Read of one of this side's messages to it,
// in the contact's current pairing; a read message was stored, so it is
// delivered too. Refused messages and anything else are left alone.
func (p *PeerMessages) PeerRead(ctx context.Context, contact, id string, at time.Time) error {
	return p.exec(ctx, contact, `UPDATE peer_messages SET state = 'read',
    read_at = COALESCE(read_at, ?3), delivered_at = COALESCE(delivered_at, ?3), class = '', reason = ''
WHERE `+currentOutbound+` AND state IN ('queued', 'expired', 'delivered', 'read')`,
		contact, id, msgStamp(at))
}

// Page is a contact's messages, both directions, of every pairing with it
// (#276), newest first, before a cursor (0 or less for the newest).
func (p *PeerMessages) Page(ctx context.Context, contact string, before int64, limit int) ([]messaging.Row, bool, error) {
	return p.page(ctx, pageQuery{contact, before, limit, ""})
}

// CurrentPage is Page in the newest pairing the contact's history holds,
// its current one while paired (#276): what a client without pairing
// history reads, never another pairing's message of the same ID.
func (p *PeerMessages) CurrentPage(ctx context.Context, contact string, before int64, limit int) ([]messaging.Row, bool, error) {
	return p.page(ctx, pageQuery{contact, before, limit, ` AND pairing_epoch = ` + historyEpochOf1})
}

// pageQuery is one page of a contact's messages: before a cursor, at most
// limit, in the pairings scope admits.
type pageQuery struct {
	contact string
	before  int64
	limit   int
	scope   string
}

func (p *PeerMessages) page(ctx context.Context, q pageQuery) ([]messaging.Row, bool, error) {
	if err := p.localOnly(); err != nil {
		return nil, false, err
	}
	limit, before := max(1, min(q.limit, messagePageLimit)), q.before
	if before <= 0 {
		before = math.MaxInt64
	}
	rows, err := queryRows(ctx, p.db, `WHERE contact = ?1 AND cursor < ?2`+q.scope+` ORDER BY cursor DESC LIMIT ?3`, q.contact, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	more := len(rows) > limit
	out := make([]messaging.Row, 0, min(len(rows), limit))
	for _, r := range rows[:min(len(rows), limit)] {
		out = append(out, r.Row)
	}
	return out, more, withSeals(ctx, p.db, out)
}

// Confirmed reports a confirmed contact: the only identities a message
// session is accepted from or dialed to.
func (p *PeerMessages) Confirmed(ctx context.Context, contact string) (bool, error) {
	return confirmedContact(ctx, p.db, contactKey(contact))
}
