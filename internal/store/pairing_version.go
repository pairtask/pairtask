package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// A bound store names the pairing it was admitted under and records the
// version each session agreed (#276): messaging finds both by type.

// PairingID is the pairing this store was admitted under, captured at
// Admit: nil for a legacy contact or the root store. Each call returns a
// fresh copy.
func (p *PeerMessages) PairingID() []byte {
	if p.fence == nil || p.fence.pairingID == "" {
		return nil
	}
	return []byte(p.fence.pairingID)
}

// MinVersion is the lowest message version this store's contact may speak:
// any for a legacy contact, PairingVersion once it holds a pairing ID.
func (p *PeerMessages) MinVersion() byte {
	if p.PairingID() == nil {
		return messaging.MinVersion
	}
	return messaging.PairingVersion
}

// SawVersion records the version a session with the contact agreed, under
// the fence, and whether it ever agreed one with seals (#115); the root
// store records nothing.
func (p *PeerMessages) SawVersion(ctx context.Context, v byte) error {
	if p.fence == nil {
		return nil
	}
	var moved bool
	err := p.inTx(ctx, p.fence.contact, func(tx *sql.Tx) error {
		before, err := signingNow(ctx, tx, p.fence.contact)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE peer_contacts SET peer_version = ? WHERE identity_key = ? AND peer_version IS NOT ?`,
			int(v), p.fence.contact, int(v)); err != nil {
			return err
		}
		if err := noteSealsSeen(ctx, tx, p.fence.contact, v); err != nil {
			return err
		}
		after, err := signingNow(ctx, tx, p.fence.contact)
		moved = before != after
		return err
	})
	p.moved = p.moved || (err == nil && moved)
	return err
}

// VersionMoved: a session through this store changed how its contact's
// version stands for signing (#115), which the operator is shown.
func (p *PeerMessages) VersionMoved() bool { return p.moved }

// signingNow is how the contact's recorded version stands for signing.
func signingNow(ctx context.Context, tx *sql.Tx, contact string) (string, error) {
	var version int
	var seen bool
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(peer_version, 0), EXISTS (SELECT 1 FROM peer_seal_seen WHERE contact = ?1)
FROM peer_contacts WHERE identity_key = ?1`, contact).Scan(&version, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return ContactSigning(version, seen), err
}

// ContactSigning is how a contact's last agreed version stands for
// signing (#115): unknown or with seals, nothing to say; below that, still
// to update while it never agreed seals, gone back once it did.
func ContactSigning(version int, sealsSeen bool) string {
	switch {
	case version <= 0 || version >= int(messaging.SealVersion):
		return ""
	case sealsSeen:
		return protocol.ContactSigningDowngraded
	}
	return protocol.ContactSigningUnsupported
}
