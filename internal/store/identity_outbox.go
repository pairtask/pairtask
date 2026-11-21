package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// The identity outbox (#392): signed statements this side owes a contact,
// sent in the identity phase of the next session (peer version 13) and
// settled by the contact's answer. Slice 2 fills it from init, link and
// revoke; until then it is filled by QueueIdentity.

var _ messaging.IdentityStore = (*PeerMessages)(nil)

// identityExchange turns the identity phase on (peer version 13). It stays
// off until the chat can show identity verdicts itself (#421 slice 2): a
// statement a peer sends must never let a row look verified that the
// identity state holds unapproved or frozen. Tests turn it on.
var identityExchange = false

// ExchangesIdentity: sessions through this store take the identity phase.
func (p *PeerMessages) ExchangesIdentity() bool { return identityExchange }

// QueueIdentity owes the contact one signed statement; the same statement
// again is queued once.
func (s *Store) QueueIdentity(ctx context.Context, contact string, statement []byte, at time.Time) error {
	if _, err := identity.Parse(statement); err != nil {
		return fmt.Errorf("%w: %v", identity.ErrMalformed, err)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO identity_outbox (contact, digest, body, queued_at) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		contact, wireDigest(statement), statement, msgStamp(at))
	return err
}

// wireDigest is how the wire names a statement: SHA-256 of its bytes.
func wireDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// PendingIdentity is up to max statements still owed contact, oldest first.
func (p *PeerMessages) PendingIdentity(ctx context.Context, contact string, max int) ([]messaging.IdentityStatement, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT body FROM identity_outbox WHERE contact = ? AND effect = '' ORDER BY queued_at, digest LIMIT ?`, contact, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []messaging.IdentityStatement
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		out = append(out, messaging.IdentityStatement{Body: body})
	}
	return out, rows.Err()
}

// IdentityAnswered settles one owed statement with the contact's answer.
func (p *PeerMessages) IdentityAnswered(ctx context.Context, contact string, a messaging.IdentityStored) error {
	return p.inTx(ctx, contact, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE identity_outbox SET effect = ? WHERE contact = ? AND digest = ?`, a.Effect, contact, a.Digest)
		return err
	})
}

// identityEffectNames maps the library's effects onto the wire's.
var identityEffectNames = map[identity.EffectKind]string{
	identity.Applied: messaging.IdentityApplied, identity.Duplicate: messaging.IdentityDuplicate,
	identity.Parked: messaging.IdentityParked, identity.FrozenEffect: messaging.IdentityFrozen,
	identity.Rejected: messaging.IdentityRejected,
}

// AcceptIdentity judges a statement from contact (peer sync). A statement
// that isn't one is rejected, never a session error: the sender learns
// nothing more than that.
func (p *PeerMessages) AcceptIdentity(ctx context.Context, contact string, body []byte, at time.Time) (string, error) {
	var effect identity.Effect
	err := p.inTx(ctx, contact, func(tx *sql.Tx) (err error) {
		effect, err = applyStatement(ctx, tx, arrival{body, at.UTC(), contact})
		return err
	})
	if err != nil && isMalformedStatement(err) {
		return messaging.IdentityRejected, nil
	}
	if err != nil {
		return "", err
	}
	return identityEffectNames[effect.Kind], nil
}

// isMalformedStatement: the bytes aren't a validly signed statement, an
// input fault, never the store's.
func isMalformedStatement(err error) bool {
	for _, e := range []error{identity.ErrMalformed, identity.ErrBadSignature, identity.ErrLimit, identity.ErrFreshKey} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
