package store

import (
	"context"
	"database/sql"
	"time"
)

// pruneClaims forgets claims and notices when their retained subject goes.
// Pending durable imports keep their subject and claims until acknowledged.
func pruneClaims(ctx context.Context, tx *sql.Tx, contact string, _ time.Time) error {
	for _, query := range []string{
		`DELETE FROM remote_claims WHERE contact=? AND NOT EXISTS
(SELECT 1 FROM peer_messages pm WHERE pm.contact=remote_claims.contact AND pm.pairing_epoch=remote_claims.pairing_epoch
AND pm.direction='out' AND pm.message_id=remote_claims.message_id)`,
		`DELETE FROM claim_notices_out WHERE contact=? AND NOT EXISTS
(SELECT 1 FROM remote_imports ri WHERE ri.contact=claim_notices_out.contact AND ri.pairing_epoch=claim_notices_out.pairing_epoch
AND ri.remote_id=claim_notices_out.remote_id)`,
	} {
		if _, err := tx.ExecContext(ctx, query, contact); err != nil {
			return err
		}
	}
	return nil
}
