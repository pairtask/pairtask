package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// Replacing a review request (#259): a new request may name one of the same
// asker's it replaces, final already, to the same reviewer and change in the
// contact's current pairing. The link is kept on this side only: the new
// request records which it replaces, the old one which replaced it, both in
// the transaction that queues the new one, and only one replacement of a
// request ever wins.

// ErrNotSupersedable: the request named can't be replaced by this one.
var ErrNotSupersedable = errors.New("that review request can't be replaced by this one: only your own final request, to the same reviewer and change, once")

// parentAsk is what a replacement is checked against.
type parentAsk struct {
	reviewer, requester, ask, supersededBy string
	settled                                bool
}

// supersedeParent links a new request to the one it replaces, refusing one
// that isn't the same asker's final request to the same reviewer and change,
// or that something else replaced.
func supersedeParent(ctx context.Context, tx *sql.Tx, o outgoing) error {
	if o.supersedes == "" {
		return nil
	}
	p, found, err := loadParentAsk(ctx, tx, o)
	if err != nil {
		return err
	}
	if !found || !p.replaceableBy(o) {
		return ErrNotSupersedable
	}
	res, err := tx.ExecContext(ctx, `UPDATE review_asks_out SET superseded_by = ?3
WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` AND ask_id = ?2 AND superseded_by = ''`, o.key.contact, o.supersedes, o.key.id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errors.Join(err, ErrNotSupersedable)
	}
	return nil
}

func loadParentAsk(ctx context.Context, tx *sql.Tx, o outgoing) (parentAsk, bool, error) {
	var p parentAsk
	err := tx.QueryRowContext(ctx, `SELECT reviewer, requester_agent, ask, superseded_by, settled_at IS NOT NULL FROM review_asks_out
WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` AND ask_id = ?2`, o.key.contact, o.supersedes).
		Scan(&p.reviewer, &p.requester, &p.ask, &p.supersededBy, &p.settled)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	return p, err == nil, err
}

// replaceableBy: final, not replaced yet, and the same asker's request to
// the same reviewer and change.
func (p parentAsk) replaceableBy(o outgoing) bool {
	if !p.replaceable() || !p.askedBySame(o) {
		return false
	}
	var ask messaging.ReviewAsk
	if err := json.Unmarshal([]byte(p.ask), &ask); err != nil {
		return false
	}
	return sameChange(ask, *o.m.Review)
}

// replaceable: final, and nothing replaced it yet.
func (p parentAsk) replaceable() bool {
	return p.settled && p.supersededBy == ""
}

// askedBySame: the same asker asked the same reviewer.
func (p parentAsk) askedBySame(o outgoing) bool {
	return p.requester == o.m.From && p.reviewer == o.m.To[0]
}

// sameChange: the same forge, repository and change, whatever the pins.
func sameChange(a, b messaging.ReviewAsk) bool {
	return a.Forge == b.Forge && a.Repository == b.Repository && a.Change == b.Change
}

// sameSupersedes refuses a retry of a request that names another request it
// replaces than the first did.
func sameSupersedes(ctx context.Context, tx *sql.Tx, o outgoing) error {
	if o.m.Review == nil {
		return nil
	}
	var held string
	err := tx.QueryRowContext(ctx, `SELECT supersedes FROM review_asks_out
WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` AND ask_id = ?2`, o.key.contact, o.key.id).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // a request since collected: nothing left to differ from
	}
	if err == nil && held != o.supersedes {
		err = messaging.ErrConflict
	}
	return err
}

// keptForReplacement keeps a settled request while a request replacing it
// directly is still open; past that, its own settled time decides.
const keptForReplacement = `NOT EXISTS (SELECT 1 FROM review_asks_out c WHERE c.contact = review_asks_out.contact
        AND c.pairing_epoch = review_asks_out.pairing_epoch AND c.supersedes = review_asks_out.ask_id AND c.settled_at IS NULL)`
