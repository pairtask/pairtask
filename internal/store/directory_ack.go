package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Whether a paired workspace has what this side shares with it (#305):
// when it answers the directory this side sent, this side keeps a digest of
// exactly that list. Their side has the current set when that digest is
// the current set's; nothing else, such as a session that merely ran,
// says so. Without an answer the digest is unknown, never an empty list's.

// DirectoryDigest names a list of agents as a set (protocol.DirectoryDigest).
func DirectoryDigest(agents []string) string { return protocol.DirectoryDigest(agents) }

// DirectoryAcked records that contact answered the directory agents, sent
// to it; changed reports a digest that differs from the one kept, and only
// once that is committed.
func (p *PeerMessages) DirectoryAcked(ctx context.Context, contact string, agents []string, at time.Time) (bool, error) {
	digest := DirectoryDigest(agents)
	var changed bool
	err := p.inTx(ctx, contact, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE peer_contacts SET directory_ack = ?, directory_ack_at = ?
WHERE identity_key = ? AND directory_ack IS NOT ?`, digest, msgStamp(at), contact, digest)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		changed = n > 0
		return err
	})
	return changed && err == nil, err
}

// SharedSnapshot is what this side shares with a contact and what the
// contact last answered, read together: the agents and their digest, the
// digest answered ("" when never) and when.
type SharedSnapshot struct {
	Agents    []string
	Digest    string
	AckDigest string
	AckAt     *time.Time
}

// Confirmed: the contact answered exactly the set shared now.
func (s SharedSnapshot) Confirmed() bool { return s.AckDigest != "" && s.AckDigest == s.Digest }

// SharedSnapshot reads both in one transaction.
func (p *PeerMessages) SharedSnapshot(ctx context.Context, contact string) (SharedSnapshot, error) {
	var snap SharedSnapshot
	err := p.inTx(ctx, contact, func(tx *sql.Tx) error {
		var err error
		if snap.Agents, err = grantedAgents(ctx, tx, contact); err != nil {
			return err
		}
		snap.Digest = DirectoryDigest(snap.Agents)
		var ack, at sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT directory_ack, directory_ack_at FROM peer_contacts WHERE identity_key = ?`, contact).Scan(&ack, &at)
		if err != nil || !ack.Valid {
			return ignoreNoRows(err)
		}
		snap.AckDigest = ack.String
		snap.AckAt, err = parseOptionalStamp(at)
		return err
	})
	return snap, err
}

func ignoreNoRows(err error) error {
	if err == sql.ErrNoRows {
		return nil
	}
	return err
}

func parseOptionalStamp(s sql.NullString) (*time.Time, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	return &t, err
}
