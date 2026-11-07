package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// A control session (#276) carries contact notices only: this side's
// pause, resume or unpair out, the peer's in. It is admitted for any
// contact row this side retains, active, paused, unpaired or unpaired by
// the other side, and bound to the contact's generation, pairing epoch and
// pairing ID as they were then. Every read and write compares all three in
// its own transaction and never refreshes them, so a session admitted
// before any change to the contact may change nothing after it.

// ErrUnpairedByThem is a contact the other side unpaired: nothing more is
// sent to it or taken from it until this side unpairs or pairs again.
var ErrUnpairedByThem = errors.New("the other side unpaired; unpair here or pair again")

// controlStore is one control session's store, bound as AdmitControl
// found the contact.
type controlStore struct {
	db    *sql.DB
	fence linkFence
}

var _ messaging.ControlStore = (*controlStore)(nil)

// AdmitControl binds a control session to a contact row this side retains,
// at its generation, pairing epoch and pairing ID; an unknown contact is
// messaging.ErrUnknownContact. Only the root store admits, and admitting
// a control session never admits a message session.
func (p *PeerMessages) AdmitControl(ctx context.Context, contact string) (messaging.ControlStore, error) {
	if p.fence != nil {
		return nil, ErrFenced
	}
	var s contactState
	err := p.inTx(ctx, contact, func(tx *sql.Tx) (err error) {
		s, err = readContactState(ctx, tx, contact)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !s.confirmed {
		return nil, messaging.ErrUnknownContact
	}
	return &controlStore{db: p.db, fence: linkFence{contact: contact, sessionAuthority: s.sessionAuthority}}, nil
}

// inTx runs fn in one transaction after the fence check: the contact still
// retained, at exactly the authority the session was admitted under.
func (c *controlStore) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := c.checkFence(ctx, tx); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *controlStore) checkFence(ctx context.Context, tx *sql.Tx) error {
	s, err := readContactState(ctx, tx, c.fence.contact)
	if err != nil {
		return err
	}
	if !s.confirmed || s.sessionAuthority != c.fence.sessionAuthority {
		return ErrFenced
	}
	return nil
}

// pairingID is the admitted pairing ID: nil for a legacy contact.
func (c *controlStore) pairingID() []byte {
	if c.fence.pairingID == "" {
		return nil
	}
	return []byte(c.fence.pairingID)
}

// idArg is a pairing ID as a query compares it with IS: NULL when empty,
// as a legacy contact's is stored.
func idArg(id []byte) any {
	if len(id) == 0 {
		return nil
	}
	return id
}

// PairingID is the pairing the session was admitted under.
func (c *controlStore) PairingID(ctx context.Context) ([]byte, error) {
	if err := c.inTx(ctx, func(*sql.Tx) error { return nil }); err != nil {
		return nil, err
	}
	return c.pairingID(), nil
}

// PendingNotice is this side's oldest pending notice for the admitted
// pairing, nil if none.
func (c *controlStore) PendingNotice(ctx context.Context) (*messaging.ContactState, error) {
	var n *messaging.ContactState
	err := c.inTx(ctx, func(tx *sql.Tx) (err error) {
		n, err = c.pendingNotice(ctx, tx)
		return err
	})
	return n, err
}

func (c *controlStore) pendingNotice(ctx context.Context, tx *sql.Tx) (*messaging.ContactState, error) {
	var gen int64
	var state string
	err := tx.QueryRowContext(ctx, `SELECT gen, contact_state FROM contact_notices_out
WHERE contact = ? AND pairing_id IS ? AND state = 'pending' ORDER BY gen LIMIT 1`, c.fence.contact, idArg(c.pairingID())).Scan(&gen, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	wire, ok := wireStates[state]
	if !ok || gen <= 0 {
		return nil, fmt.Errorf("contact notice %d: state %q", gen, state)
	}
	return &messaging.ContactState{PairingID: c.pairingID(), Gen: uint64(gen), State: wire}, nil
}

// NoticeStored marks this side's notice delivered on the peer's answer,
// which must name exactly its pairing and generation: any other answer is
// messaging.ErrProtocol and changes nothing. The same answer again, once
// delivered, changes nothing either.
func (c *controlStore) NoticeStored(ctx context.Context, a messaging.ContactStateStored) error {
	if !bytes.Equal(a.PairingID, c.pairingID()) {
		return fmt.Errorf("%w: an answer for another pairing", messaging.ErrProtocol)
	}
	return c.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE contact_notices_out SET
    delivered_at = CASE WHEN state = 'pending' THEN ?4 ELSE delivered_at END,
    state = CASE WHEN state = 'pending' THEN 'delivered' ELSE state END
WHERE contact = ?1 AND pairing_id IS ?2 AND gen = ?3`, c.fence.contact, idArg(a.PairingID), storedGen(a.Gen), msgStamp(time.Now()))
		if err != nil {
			return err
		}
		return requireOne(res, "an answer for no notice of this side's")
	})
}

// storedGen is a wire generation as the store compares it: one beyond
// int64 matches no row.
func storedGen(gen uint64) int64 {
	if gen > maxStoredGen {
		return -1
	}
	return int64(gen)
}

// requireOne: the statement changed exactly one row, else a protocol error.
func requireOne(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: %s", messaging.ErrProtocol, what)
	}
	return nil
}
