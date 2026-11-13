package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

var stored = messaging.Answer{Stored: true}

func refused(reason string) messaging.Answer {
	return messaging.Answer{Refusal: messaging.Refuse(reason)}
}

// Accept decides one inbound message in one transaction: a replay is
// answered first, even with the inbox full; then the replay floor; then
// validity and quota; only then is it stored. It speaks the newest
// session version.
func (p *PeerMessages) Accept(ctx context.Context, contact string, m messaging.Message, now time.Time) (messaging.Answer, error) {
	return p.AcceptVersion(ctx, contact, messaging.Arrival{Version: 0, Message: m, At: now})
}

// AcceptVersion is Accept for a session at version, 0 the newest: from
// messaging.ConsentPeerVersion on, a request refused only as not shared
// may be held for this side's operator instead (#293).
func (p *PeerMessages) AcceptVersion(ctx context.Context, contact string, a messaging.Arrival) (messaging.Answer, error) {
	version, m, now := a.Version, a.Message, a.At
	var answer messaging.Answer
	err := p.inTx(ctx, contact, func(tx *sql.Tx) error {
		row := newRow{key: messageKey{contact, messaging.DirIn, m.ID}, m: m, now: now}
		a, inserted, err := acceptIn(ctx, tx, inboundRow{row, version, p.gate})
		answer = a
		if err == nil && !inserted {
			return errNothingStored // answered without a row: roll back
		}
		if err != nil {
			return err
		}
		return expireUnacked(ctx, tx, contact, now)
	})
	switch {
	case errors.Is(err, errNothingStored):
		return answer, nil
	case err != nil:
		return messaging.Answer{}, err // never Stored or Recorded unless it committed
	}
	return answer, nil
}

// errNothingStored ends a transaction that answered without storing.
var errNothingStored = errors.New("nothing stored")

// inboundRow is an inbound message and the version of its session.
type inboundRow struct {
	newRow
	version int
	gate    *consentGate // whether session grants may admit now (#293)
}

// acceptIn is Accept inside its transaction; inserted reports a new row to
// commit. A replay, of a stored or a held message, is answered as before
// and stores nothing.
func acceptIn(ctx context.Context, tx *sql.Tx, in inboundRow) (messaging.Answer, bool, error) {
	r := in.newRow
	if err := requireContact(ctx, tx, contactKey(r.key.contact)); err != nil {
		return messaging.Answer{}, false, err
	}
	if answer, known, err := replay(ctx, tx, r.key.contact, r.m); known || err != nil {
		return answer, false, err
	}
	if answer, known, err := consentReplay(ctx, tx, r); known || err != nil {
		return answer, false, err
	}
	answer, err := decideInbound(ctx, tx, r.key.contact, r.m)
	if err != nil {
		return messaging.Answer{}, false, err
	}
	return answerInbound(ctx, tx, in, answer)
}

// answerInbound acts on a new message's answer: stored, held for the
// operator, recorded as refused, or only refused.
func answerInbound(ctx context.Context, tx *sql.Tx, in inboundRow, answer messaging.Answer) (messaging.Answer, bool, error) {
	answer, err := publicationConsent(ctx, tx, in, answer)
	if err != nil {
		return messaging.Answer{}, false, err
	}
	switch {
	case answer.Stored:
		return insertAccepted(ctx, tx, in.newRow)
	case answer.Reason == messaging.ReasonNotShared && consentVersion(in.version):
		return holdOrRefuse(ctx, tx, in, answer)
	}
	return refuseNotShared(ctx, tx, in.newRow, answer)
}

// publicationConsent is the answer to a review request its agent's grant
// allows but whose asked publication it doesn't (#293): not shared when
// that publication was explicitly denied, or when it may be held or
// admitted for consent; else stored as before, published at most as the
// grant allows. A lower grant never silently grants publication.
func publicationConsent(ctx context.Context, tx *sql.Tx, in inboundRow, answer messaging.Answer) (messaging.Answer, error) {
	if !answer.Stored {
		return answer, nil
	}
	wider, err := widerPublication(ctx, tx, in.key.contact, in.m)
	if err != nil || !wider {
		return answer, err
	}
	reroute, err := publicationRerouted(ctx, tx, in)
	if err != nil || !reroute {
		return answer, err
	}
	return refused(messaging.ReasonNotShared), nil
}

// widerPublication: a review request asks to publish past what its
// agent's grant allows.
func widerPublication(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
	if m.Review == nil || !m.Review.Publish {
		return false, nil
	}
	g, granted, err := reviewGrant(ctx, tx, contact, m.To[0])
	if err != nil || !granted {
		return false, err
	}
	return !g.allows(protocol.ReviewPolicy{Publish: true, Formal: m.Review.Formal}), nil
}

// publicationRerouted: the publication was explicitly denied, or a session
// that may be asked can hold or admit it.
func publicationRerouted(ctx context.Context, tx *sql.Tx, in inboundRow) (bool, error) {
	sc := consentScope{in.key.contact, in.m.To[0], protocol.ConsentPublish}
	denied, err := explicitlyDenied(ctx, tx, sc, in.m.Review)
	if err != nil || denied {
		return denied, err
	}
	if !consentVersion(in.version) {
		return false, nil
	}
	a, err := admissionOf(ctx, tx, in)
	return a.kind != admitNone, err
}

// insertAccepted stores an accepted message and stages it for this side's
// agents when it is theirs.
func insertAccepted(ctx context.Context, tx *sql.Tx, r newRow) (messaging.Answer, bool, error) {
	if err := insertMessage(ctx, tx, r); err != nil {
		return messaging.Answer{}, false, err // never Stored when the insert failed
	}
	if err := keepSeal(ctx, tx, r.key, r.m.Seal); err != nil {
		return messaging.Answer{}, false, err
	}
	if err := stageImport(ctx, tx, r); err != nil {
		return messaging.Answer{}, false, err
	}
	return stored, true, nil
}

// inboundCheck refuses a message for one reason, or passes it on.
type inboundCheck struct {
	reason  string
	refuses func(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error)
}

// inboundChecks run in order after the replay lookup: identity and
// routing, the recipients' grants, the replay floor, the text, then the
// quota.
var inboundChecks = []inboundCheck{
	{messaging.ReasonInvalid, func(_ context.Context, _ *sql.Tx, _ string, m messaging.Message) (bool, error) {
		if !validSequence(m.N) || !messaging.ValidID(m.ID) {
			return true, nil
		}
		return !messaging.ValidRouting(m) || !validAsk(m), nil
	}},
	{messaging.ReasonInvalidSeal, sealRefuses},
	{messaging.ReasonNotShared, func(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
		ok, err := recipientsGranted(ctx, tx, contact, m)
		return !ok, err
	}},
	{messaging.ReasonConflict, func(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
		return importedBefore(ctx, tx, contact, m)
	}},
	{messaging.ReasonTooOld, func(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
		return belowFloor(ctx, tx, contact, m.N)
	}},
	{messaging.ReasonTooLarge, func(_ context.Context, _ *sql.Tx, _ string, m messaging.Message) (bool, error) {
		return len(m.Text) > messaging.TextBytes, nil
	}},
	{messaging.ReasonInvalid, func(_ context.Context, _ *sql.Tx, _ string, m messaging.Message) (bool, error) {
		return !messaging.ValidText(m.Text), nil
	}},
	{messaging.ReasonInboxFull, func(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
		if len(m.To) > 0 {
			return importBacklogFull(ctx, tx, contact, len(m.Text))
		}
		return peerInboxFull(ctx, tx, contact, len(m.Text))
	}},
	{messaging.ReasonInboxFull, func(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
		if m.Review == nil {
			return false, nil
		}
		if full, err := outcomesFull(ctx, tx, contact); full || err != nil {
			return full, err
		}
		return asksFull(ctx, tx, contact, m)
	}},
}

// recipientsGranted: a message's recipients are shared with the contact;
// a review request's one agent may be asked by it, whatever it shares.
func recipientsGranted(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
	if m.Review == nil {
		return allGranted(ctx, tx, contact, m.To)
	}
	_, granted, err := reviewGrant(ctx, tx, contact, m.To[0])
	return granted, err
}

// decideInbound is the answer to a message that is not a replay; Stored
// means it may be inserted.
func decideInbound(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (messaging.Answer, error) {
	return runChecks(ctx, tx, inboundChecks, newRow{key: messageKey{contact, messaging.DirIn, m.ID}, m: m})
}

// validSequence: from 1, within what the store can hold.
func validSequence(n uint64) bool { return n != 0 && n < math.MaxInt64 }

// replay answers a message whose ID or sequence is already held in the
// contact's current pairing: the same message is Stored again, anything
// else is a conflict.
func replay(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (messaging.Answer, bool, error) {
	row, found, err := messageRow(ctx, tx, messageKey{contact, messaging.DirIn, m.ID})
	if err != nil {
		return messaging.Answer{}, false, err
	}
	if found {
		return replayedSeal(ctx, tx, messageKey{contact, messaging.DirIn, m.ID}, sameMessage(row, m), m.Seal)
	}
	if !validSequence(m.N) {
		return messaging.Answer{}, false, nil
	}
	var other int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM peer_messages WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+`
    AND direction = 'in' AND n = ?2`, contact, int64(m.N)).Scan(&other)
	if err != nil || other == 0 {
		return messaging.Answer{}, false, err
	}
	return refused(messaging.ReasonConflict), true, nil
}

// sameMessage answers a message whose ID is held: the very same message is
// answered as before, Stored again or refused as recorded; anything else
// is a conflict.
func sameMessage(row storedRow, m messaging.Message) messaging.Answer {
	hash := m.Hash()
	switch {
	case string(row.hash) != string(hash[:]):
		return refused(messaging.ReasonConflict)
	case row.State == messaging.StateRefused:
		return messaging.Answer{Refusal: row.Refusal}
	}
	return stored
}

// belowFloor: the sequence is older than anything this side still
// remembers, so it can't be told apart from a replay.
func belowFloor(ctx context.Context, tx *sql.Tx, contact string, n uint64) (bool, error) {
	floor, err := replayFloor(ctx, tx, contact)
	return int64(n) < floor, err
}

// replayFloor is the contact's replay floor in its current pairing.
func replayFloor(ctx context.Context, q rowQuerier, contact string) (int64, error) {
	var floor int64
	err := q.QueryRowContext(ctx, `SELECT floor FROM peer_message_floors WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1,
		contact).Scan(&floor)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return floor, err
}

// peerInboxFull: the unread messages of the contact's current pairing, or
// the remembered identities, are at their bounds.
func peerInboxFull(ctx context.Context, tx *sql.Tx, contact string, size int) (bool, error) {
	var unread, unreadBytes int
	err := tx.QueryRowContext(ctx, `SELECT
    COALESCE(SUM(state = 'received'), 0), COALESCE(SUM(CASE WHEN state = 'received' THEN text_bytes END), 0)
FROM peer_messages WHERE `+currentInbound, contact).Scan(&unread, &unreadBytes)
	if err != nil || !unreadBudget.holds(budget{unread, unreadBytes + size}) {
		return true, err
	}
	return identitiesFull(ctx, tx, contact)
}

// MarkRead marks inbound messages of the contact's current pairing read as
// shown here, and queues a receipt for each one newly read; the oldest
// pending receipts past the bound are dropped.
func (p *PeerMessages) MarkRead(ctx context.Context, contact string, ids []string, at time.Time) (int, error) {
	return p.markRead(ctx, ReadMark{Contact: contact, IDs: ids, At: at}, currentMark)
}

// ReadMark is messages of one of the contact's pairings, named by its
// epoch, shown to the operator at a time (#276).
type ReadMark struct {
	Contact string
	Epoch   int64
	IDs     []string
	At      time.Time
}

// MarkReadIn marks inbound messages of the pairing r names read as shown
// here: the current one as MarkRead does, an earlier one, or any of an
// unpaired or removed contact, as history read here alone, with no receipt
// for the peer (#276). An epoch past the newest the contact's history
// holds, or below 1, is messaging.ErrInvalid, marking nothing.
func (p *PeerMessages) MarkReadIn(ctx context.Context, r ReadMark) (int, error) {
	return p.markRead(ctx, r, explicitMark)
}

// markScope is the pairing a mark is read in: as named, or the current one.
type markScope func(ctx context.Context, tx *sql.Tx, r ReadMark) (ReadMark, error)

// readScope is the scope of a read naming epoch: the current pairing for
// 0, else the one named (#276).
func readScope(epoch int64) markScope {
	if epoch == 0 {
		return currentMark
	}
	return explicitMark
}

// explicitMark is the mark as named.
func explicitMark(_ context.Context, _ *sql.Tx, r ReadMark) (ReadMark, error) { return r, nil }

// currentMark is the mark in its contact's current pairing.
func currentMark(ctx context.Context, tx *sql.Tx, r ReadMark) (ReadMark, error) {
	var err error
	r.Epoch, err = contactEpoch(ctx, tx, r.Contact)
	return r, err
}

// markRead marks a mark read, in the pairing scope picks, in one
// transaction.
func (p *PeerMessages) markRead(ctx context.Context, r ReadMark, scope markScope) (int, error) {
	if err := p.localOnly(); err != nil {
		return 0, err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if r, err = scope(ctx, tx, r); err != nil {
		return 0, err
	}
	marked, err := markReadIn(ctx, tx, r)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return marked, nil
}

// markReadIn is MarkReadIn inside a transaction.
func markReadIn(ctx context.Context, tx *sql.Tx, r ReadMark) (int, error) {
	k := pairingKey{r.Contact, r.Epoch}
	receipt, err := readReceipt(ctx, tx, k)
	if err != nil {
		return 0, err
	}
	marked := 0
	for _, id := range r.IDs {
		n, err := markOneRead(ctx, tx, readOne{k, id, r.At, receipt})
		if err != nil {
			return 0, err
		}
		marked += n
	}
	if err := dropReceiptsOver(ctx, tx, r.Contact); err != nil {
		return 0, err
	}
	return marked, nil
}

// readReceipt is the receipt a read in pairing k owes the peer: pending in
// the live pairing alone; local only in an earlier one, for an unpaired
// contact, or one removed, whose history is read here alone (#276).
func readReceipt(ctx context.Context, tx *sql.Tx, k pairingKey) (string, error) {
	live, err := heldPairing(ctx, tx, k)
	if err != nil || !live {
		return "local", err
	}
	return "pending", nil
}

// readOne is one inbound message of a pairing shown to the operator at a
// time, and the receipt its read owes.
type readOne struct {
	k       pairingKey
	id      string
	at      time.Time
	receipt string
}

// markOneRead marks a received message of the pairing read, with the
// receipt its read owes, or a refusal recorded here read once, with none:
// the peer already knows. Another pairing's message with the same ID is
// never touched (#276).
func markOneRead(ctx context.Context, tx *sql.Tx, r readOne) (int, error) {
	res, err := tx.ExecContext(ctx, `UPDATE peer_messages SET
    state = CASE WHEN state = 'received' THEN 'read' ELSE state END, read_at = ?3,
    receipt = CASE WHEN state = 'received' THEN ?4 ELSE receipt END,
    receipt_at = CASE WHEN state = 'received' THEN ?3 ELSE receipt_at END
WHERE contact = ?1 AND pairing_epoch = ?2 AND direction = 'in' AND message_id = ?5
    AND (state = 'received' OR state = 'refused' AND read_at IS NULL)`, r.k.contact, r.k.epoch, msgStamp(r.at), r.receipt, r.id)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// dropReceiptsOver drops the oldest pending receipts of the contact's
// current pairing beyond the bound.
func dropReceiptsOver(ctx context.Context, tx *sql.Tx, contact string) error {
	_, err := tx.ExecContext(ctx, `UPDATE peer_messages SET receipt = 'dropped'
WHERE cursor IN (SELECT cursor FROM peer_messages WHERE `+currentInbound+` AND receipt = 'pending'
    ORDER BY receipt_at DESC, cursor DESC LIMIT -1 OFFSET ?2)`, contact, messaging.Receipts)
	return err
}

// PendingReads is the oldest receipts of the contact's current pairing not
// yet confirmed by the peer.
func (p *PeerMessages) PendingReads(ctx context.Context, contact string, max int) ([]string, error) {
	var ids []string
	err := p.inTx(ctx, contact, func(tx *sql.Tx) (err error) {
		ids, err = queryStrings(ctx, tx, `SELECT message_id FROM peer_messages
WHERE `+currentInbound+` AND receipt = 'pending' ORDER BY receipt_at, cursor LIMIT ?2`,
			contact, sessionBudget.clamp(budget{max, 0}).count)
		return err
	})
	return ids, err
}

// ReadConfirmed records the peer's ReadStored for a pending receipt of the
// contact's current pairing.
func (p *PeerMessages) ReadConfirmed(ctx context.Context, contact, id string) error {
	return p.exec(ctx, contact, `UPDATE peer_messages SET receipt = 'confirmed'
WHERE `+currentInbound+` AND message_id = ?2 AND receipt = 'pending'`, contact, id)
}

// currentInbound is the messages from contact ?1 in its current pairing
// (#276).
const currentInbound = `contact = ?1 AND pairing_epoch = ` + currentEpochOf1 + ` AND direction = 'in'`
