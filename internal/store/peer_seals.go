package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

// Seals across pairing links (#115). A message to or from a contact keeps
// its sender's seal beside it, in its own tables so released ones keep
// their shape. The receiving side verifies every inbound seal against the
// keys it pinned for that contact (its workspace key while paired, and the
// person keys its operator accepted for it) and refuses one that doesn't
// hold or doesn't sign exactly this message from this contact to this
// side. A person key it hasn't accepted is recorded as pending, for the
// operator to accept or not; it is never trusted on first sight.

const peerSealsSchema = `
CREATE TABLE IF NOT EXISTS peer_message_seals (
    contact    TEXT NOT NULL,
    direction  TEXT NOT NULL,
    message_id TEXT NOT NULL,
    seal       BLOB NOT NULL,
    downgraded INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (contact, direction, message_id)
);
CREATE TABLE IF NOT EXISTS peer_seal_policy (
    contact TEXT PRIMARY KEY,
    require INTEGER NOT NULL,
    set_at  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS peer_person_keys (
    contact    TEXT NOT NULL,
    key_id     TEXT NOT NULL,
    first_seen TEXT NOT NULL,
    PRIMARY KEY (contact, key_id)
);
`

// PeerSealer seals a new outbound message from an agent, once its
// sequence and time are fixed: the workspace's broker attesting the agent.
type PeerSealer func(contact string, m messaging.Message) []byte

// keepSeal stores a message's seal beside it; the same message again with
// another seal is a conflict, and none keeps whatever is stored.
func keepSeal(ctx context.Context, tx *sql.Tx, key messageKey, seal []byte) error {
	if len(seal) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO peer_message_seals (contact, direction, message_id, seal) VALUES (?, ?, ?, ?)
		ON CONFLICT DO NOTHING`, key.contact, key.direction, key.id, seal); err != nil {
		return err
	}
	var held []byte
	if err := tx.QueryRowContext(ctx, `SELECT seal FROM peer_message_seals WHERE contact = ? AND direction = ? AND message_id = ?`,
		key.contact, key.direction, key.id).Scan(&held); err != nil {
		return err
	}
	if !slices.Equal(held, seal) {
		return fmt.Errorf("%w: the same message with another seal", messaging.ErrConflict)
	}
	return nil
}

// withSeals gives rows their seals, and whether an outbound one's seal
// didn't travel.
func withSeals(ctx context.Context, q rowQuerier, rows []messaging.Row) error {
	for i := range rows {
		r := &rows[i]
		err := q.QueryRowContext(ctx, `SELECT seal, downgraded FROM peer_message_seals WHERE contact = ? AND direction = ? AND message_id = ?`,
			r.Contact, r.Direction, r.ID).Scan(&r.Seal, &r.SealDowngraded)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

// SealDowngraded records that an outbound message's seal didn't travel:
// the contact's version predates seals.
func (p *PeerMessages) SealDowngraded(ctx context.Context, contact, id string) error {
	_, err := p.db.ExecContext(ctx, `UPDATE peer_message_seals SET downgraded = 1 WHERE contact = ? AND direction = ? AND message_id = ?`,
		contact, messaging.DirOut, id)
	return err
}

// sealRefuses checks an inbound message's seal, when it has one: refused
// when it doesn't hold or doesn't bind this message, this contact and this
// side; a person key not accepted for the contact is recorded as pending.
func sealRefuses(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
	if len(m.Seal) == 0 {
		return false, nil
	}
	seal, err := signing.ParseSeal(m.Seal)
	if err != nil {
		return true, nil
	}
	own, known, err := ownIdentity(ctx, tx)
	if err != nil {
		return false, err
	}
	if !known {
		return true, nil // a side without an identity can't be addressed
	}
	roots, err := contactRoots(ctx, tx, contact)
	if err != nil {
		return false, err
	}
	result := signing.Verify(&seal, roots, time.Now())
	if result.State == signing.Invalid || !sealBinds(seal, contact, own, m) {
		return true, nil
	}
	return false, notePersonKey(ctx, tx, contact, seal, result)
}

// sealBinds: the seal signs this message from this contact, to exactly the
// recipients and reply the frame names.
func sealBinds(seal signing.Seal, contact, own string, m messaging.Message) bool {
	h := seal.Header
	kind, id := signing.SenderAgent, m.From
	if m.From == "" {
		kind, id = signing.SenderOperator, protocol.OperatorID
	}
	frame := signing.Frame{MessageID: m.ID, SenderKind: kind, SenderID: id, Text: m.Text, Addresses: []string{protocol.PeerWorkspaceRecipient(own)}}
	addressed := slices.Equal(h.Recipients, protocol.PeerSealRecipients(own, m.To))
	return h.Workspace == contact && addressed && h.InReplyTo == protocol.PeerReplyRef(m.ReplyTo.ID, m.ReplyTo.Author) && seal.Matches(frame) == nil
}

// ownIdentity is this workspace's identity key, once it has one.
func ownIdentity(ctx context.Context, q rowQuerier) (string, bool, error) {
	var fp string
	err := q.QueryRowContext(ctx, `SELECT fingerprint FROM peer_identity WHERE id = 1`).Scan(&fp)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return fp, err == nil, err
}

// contactRoots are the keys a contact's seals verify against here: its
// workspace key, and the person keys pinned for it.
func contactRoots(ctx context.Context, tx *sql.Tx, contact string) (signing.Roots, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key_id, person, workspace, approved FROM signing_roots WHERE workspace = ?`, contact)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roots := []protocol.SigningRoot{{KeyID: contact, Workspace: contact, Approved: true}}
	for rows.Next() {
		var r protocol.SigningRoot
		if err := rows.Scan(&r.KeyID, &r.Person, &r.Workspace, &r.Approved); err != nil {
			return nil, err
		}
		roots = append(roots, r)
	}
	return protocol.RootsOf(roots), rows.Err()
}

// notePersonKey records the person key behind an operator seal that this
// side hasn't accepted for the contact.
func notePersonKey(ctx context.Context, tx *sql.Tx, contact string, seal signing.Seal, result signing.Result) error {
	if result.State != signing.Unverified || result.Signer == nil || result.Signer.Basis != signing.BasisPerson {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO peer_person_keys (contact, key_id, first_seen) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
		contact, seal.Chain[0].Issuer, stamp(time.Now()))
	return err
}

// PendingPersonKeys are the person keys a contact signed with that its
// operator hasn't accepted here, oldest first.
func (p *PeerMessages) PendingPersonKeys(ctx context.Context, contact string) ([]string, error) {
	return queryStrings(ctx, p.db, `SELECT key_id FROM peer_person_keys k WHERE contact = ?1
		AND NOT EXISTS (SELECT 1 FROM signing_roots r WHERE r.key_id = k.key_id AND r.workspace = ?1 AND r.approved)
		ORDER BY first_seen, key_id`, contact)
}

// ErrNoSuchPersonKey: the contact never signed with that key here.
var ErrNoSuchPersonKey = errors.New("store: no pending person key for that contact")

// AcceptPersonKey is the operator's acceptance of a contact's person key:
// one the contact signed with here, pinned for that contact alone.
func (s *Store) AcceptPersonKey(ctx context.Context, contact, keyID string, at time.Time) error {
	var seen int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM peer_person_keys WHERE contact = ? AND key_id = ?`, contact, keyID).Scan(&seen); err != nil {
		return err
	}
	if seen == 0 {
		return ErrNoSuchPersonKey
	}
	return s.PinSigningRoot(ctx, protocol.SigningRoot{KeyID: keyID, Person: true, Workspace: contact, Approved: true}, at)
}

// replayedSeal checks a replay's seal against the one stored: another
// non-empty seal for the same message is a conflict; a message stored
// unsigned stays so.
func replayedSeal(ctx context.Context, tx *sql.Tx, key messageKey, answer messaging.Answer, seal []byte) (messaging.Answer, bool, error) {
	if !answer.Stored || len(seal) == 0 {
		return answer, true, nil
	}
	var held []byte
	err := tx.QueryRowContext(ctx, `SELECT seal FROM peer_message_seals WHERE contact = ? AND direction = ? AND message_id = ?`,
		key.contact, key.direction, key.id).Scan(&held)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return answer, true, nil
	case err != nil:
		return messaging.Answer{}, true, err
	case !slices.Equal(held, seal):
		return refused(messaging.ReasonConflict), true, nil
	}
	return answer, true, nil
}

// sealText is a stored seal as a frame carries it (base64), "" for none.
func sealText(ctx context.Context, tx *sql.Tx, key messageKey) (string, error) {
	var seal []byte
	err := tx.QueryRowContext(ctx, `SELECT seal FROM peer_message_seals WHERE contact = ? AND direction = ? AND message_id = ?`,
		key.contact, key.direction, key.id).Scan(&seal)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return base64.StdEncoding.EncodeToString(seal), err
}

// ContactSigningRoots are the workspace keys of the contacts this side is
// paired with, confirmed and not unpaired: each attests its own agents'
// messages here. A paused contact keeps its root, so what it sent before
// still verifies.
func (s *Store) ContactSigningRoots(ctx context.Context) ([]protocol.SigningRoot, error) {
	keys, err := queryStrings(ctx, s.db, `SELECT identity_key FROM peer_contacts WHERE unpaired_at = '' ORDER BY identity_key`)
	if err != nil {
		return nil, err
	}
	roots := make([]protocol.SigningRoot, 0, len(keys))
	for _, key := range keys {
		confirmed, err := confirmedContact(ctx, s.db, contactKey(key))
		if err != nil {
			return nil, err
		}
		if confirmed {
			roots = append(roots, protocol.SigningRoot{KeyID: key, Workspace: key, Approved: true})
		}
	}
	return roots, nil
}

// SetRequireSeals is the operator's choice for a contact: refuse its
// unsigned messages, or take them marked unsigned (the default).
func (s *Store) SetRequireSeals(ctx context.Context, contact string, require bool, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO peer_seal_policy (contact, require, set_at) VALUES (?, ?, ?)
		ON CONFLICT(contact) DO UPDATE SET require = excluded.require, set_at = excluded.set_at`, contact, require, stamp(at))
	return err
}

// RequiresSeals: this side refuses the contact's unsigned messages.
func (p *PeerMessages) RequiresSeals(ctx context.Context, contact string) (bool, error) {
	return requiresSeals(ctx, p.db, contact)
}

func requiresSeals(ctx context.Context, q rowQuerier, contact string) (bool, error) {
	var require bool
	err := q.QueryRowContext(ctx, `SELECT require FROM peer_seal_policy WHERE contact = ?`, contact).Scan(&require)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return require, err
}

// unsignedRefused refuses an unsigned message from a contact this side
// requires signed: as unsigned to a peer that knows the reason, as invalid
// to one whose version predates seals.
func unsignedRefused(ctx context.Context, tx *sql.Tx, in inboundRow) (messaging.Answer, bool, error) {
	if len(in.m.Seal) > 0 {
		return messaging.Answer{}, false, nil
	}
	require, err := requiresSeals(ctx, tx, in.key.contact)
	if err != nil || !require {
		return messaging.Answer{}, false, err
	}
	if in.version != 0 && in.version < int(messaging.SealVersion) {
		return refused(messaging.ReasonInvalid), true, nil
	}
	return refused(messaging.ReasonUnsigned), true, nil
}
