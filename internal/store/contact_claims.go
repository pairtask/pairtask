package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// ContactClaims adds claims to an operator's contact history. Remote claims
// belong only to their exact message and current pairing; local claims keep
// their local provenance. This read neither acknowledges nor changes anything.
func (s *Store) ContactClaims(ctx context.Context, row messaging.Row) (*protocol.ClaimPage, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if row.Direction == messaging.DirOut {
		return contactRemoteClaims(ctx, tx, row)
	}
	var localID string
	err = tx.QueryRowContext(ctx, `SELECT local_id FROM remote_imports WHERE contact=? AND pairing_epoch=? AND remote_id=?`, row.Contact, row.PairingEpoch, row.ID).Scan(&localID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	set, err := messageClaims(ctx, tx, localID)
	if err != nil {
		return nil, err
	}
	set = pairedSet(set, localID)
	return nonemptyClaims(set.ClaimPage), nil
}

func contactRemoteClaims(ctx context.Context, tx *sql.Tx, row messaging.Row) (*protocol.ClaimPage, error) {
	rows, err := tx.QueryContext(ctx, `SELECT rc.contact, rc.agent, rc.role, rc.task, rc.state, rc.at FROM remote_claims rc
JOIN peer_contacts c ON c.identity_key=rc.contact AND c.pairing_epoch=rc.pairing_epoch AND c.unpaired_at=''
WHERE rc.contact=? AND rc.pairing_epoch=? AND rc.message_id=?
ORDER BY rc.state='released',rc.at,rc.task,rc.role,rc.agent`, row.Contact, row.PairingEpoch, row.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page, err := remoteClaimPage(rows, row.ID)
	return nonemptyClaims(page), err
}

func nonemptyClaims(page protocol.ClaimPage) *protocol.ClaimPage {
	if page.Total == 0 {
		return nil
	}
	return &page
}
