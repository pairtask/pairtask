package store

import (
	"context"
	"database/sql"

	"github.com/pairtask/pairtask/internal/peer/messaging"
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
// the fence; the root store records nothing.
func (p *PeerMessages) SawVersion(ctx context.Context, v byte) error {
	if p.fence == nil {
		return nil
	}
	return p.inTx(ctx, p.fence.contact, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE peer_contacts SET peer_version = ? WHERE identity_key = ? AND peer_version IS NOT ?`,
			int(v), p.fence.contact, int(v))
		return err
	})
}
