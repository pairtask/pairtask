package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// The peer's notices, within one pairing ID (#276): a pause or resume
// applies only above the generation of the state last stored, an unpair is
// terminal for that ID, and any other notice is answered and changes
// nothing. A remote unpair ends this side's authority for the contact in
// the transaction that stores it: the generation and the authority mark
// move on and the queue ends, while grants, consent and history stay as
// evidence until this side's operator unpairs.

// maxStoredGen is the highest generation the store holds (Amendment B).
const maxStoredGen = uint64(math.MaxInt64)

// wireStates are the stored names of the notice states, and peerStates
// their inverse.
var (
	wireStates = map[string]byte{"paused": messaging.ContactStatePaused, "resumed": messaging.ContactStateResumed,
		remoteUnpaired: messaging.ContactStateUnpaired}
	peerStates = map[byte]string{messaging.ContactStatePaused: "paused", messaging.ContactStateResumed: "resumed",
		messaging.ContactStateUnpaired: remoteUnpaired}
)

// peerStateRow is the peer's state as this side last stored it.
type peerStateRow struct {
	state string
	gen   int64
}

// peerNotice is one peer notice as the store weighs it: its generation
// and its state's stored name.
type peerNotice struct {
	gen   int64
	state string
}

// AcceptNotice stores the peer's notice by the ordering rules and answers
// the acknowledgement to send. A generation beyond the store's range or an
// unknown state is messaging.ErrProtocol and another pairing ID is
// messaging.ErrOtherPairing, both changing nothing. Close is a remote
// unpair, stored now or before.
func (c *controlStore) AcceptNotice(ctx context.Context, n messaging.ContactState) (messaging.ControlAnswer, error) {
	answer := messaging.ControlAnswer{Ack: messaging.ContactStateStored{PairingID: n.PairingID, Gen: n.Gen}}
	err := c.inTx(ctx, func(tx *sql.Tx) (err error) {
		in, err := c.weigh(n)
		if err != nil {
			return err
		}
		answer.Close, err = c.accept(ctx, tx, in)
		return err
	})
	if err != nil {
		return messaging.ControlAnswer{}, err
	}
	return answer, nil
}

// weigh checks a notice against the admitted pairing and the store's range.
func (c *controlStore) weigh(n messaging.ContactState) (peerNotice, error) {
	state, known := peerStates[n.State]
	if !known || n.Gen == 0 || n.Gen > maxStoredGen {
		return peerNotice{}, fmt.Errorf("%w: contact notice generation or state", messaging.ErrProtocol)
	}
	if !bytes.Equal(n.PairingID, c.pairingID()) {
		return peerNotice{}, messaging.ErrOtherPairing
	}
	return peerNotice{gen: int64(n.Gen), state: state}, nil
}

// accept applies a notice the ordering admits; closed: the pairing is
// unpaired by the other side, by this notice or an earlier one.
func (c *controlStore) accept(ctx context.Context, tx *sql.Tx, in peerNotice) (closed bool, err error) {
	stored, err := c.peerState(ctx, tx)
	if err != nil {
		return false, err
	}
	if stored.state == remoteUnpaired {
		return in.state == remoteUnpaired, nil
	}
	if in.gen <= stored.gen {
		c.ignored(stored, in)
		return false, nil
	}
	if in.state == remoteUnpaired {
		return true, c.unpairedByThem(ctx, tx, in)
	}
	return false, c.setPeerState(ctx, tx, in)
}

func (c *controlStore) peerState(ctx context.Context, tx *sql.Tx) (peerStateRow, error) {
	var s peerStateRow
	err := tx.QueryRowContext(ctx, `SELECT peer_state, peer_state_gen FROM peer_contacts WHERE identity_key = ?`, c.fence.contact).
		Scan(&s.state, &s.gen)
	return s, err
}

// ignored logs a notice that names the stored generation with another
// state; an older one, or an exact repeat, is simply answered again.
func (c *controlStore) ignored(stored peerStateRow, in peerNotice) {
	if in.gen == stored.gen && in.state != stored.state {
		log.Printf("store: contact %s: notice %q at generation %d conflicts with the stored %q; ignored",
			c.fence.contact, in.state, in.gen, stored.state)
	}
}

// setPeerState stores the peer's pause or resume; nothing else changes.
func (c *controlStore) setPeerState(ctx context.Context, tx *sql.Tx, in peerNotice) error {
	_, err := tx.ExecContext(ctx, `UPDATE peer_contacts SET peer_state = ?, peer_state_gen = ?, peer_state_at = ? WHERE identity_key = ?`,
		in.state, in.gen, msgStamp(time.Now()), c.fence.contact)
	return err
}

// unpairedByThem stores a remote unpair: the generation and the authority
// mark move on, never past their maximum, and the contact's queued
// messages of the current epoch end.
func (c *controlStore) unpairedByThem(ctx context.Context, tx *sql.Tx, in peerNotice) error {
	mark, err := nextMark(ctx, tx, c.fence.generation)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE peer_contacts SET generation = generation + 1, authority_mark = ?,
    peer_state = ?, peer_state_gen = ?, peer_state_at = ? WHERE identity_key = ?`,
		mark, in.state, in.gen, msgStamp(time.Now()), c.fence.contact); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE peer_messages SET state = 'expired', reason = 'unpaired_by_them'
WHERE contact = ? AND pairing_epoch = ? AND direction = 'out' AND state = 'queued'`, c.fence.contact, c.fence.epoch)
	return err
}
