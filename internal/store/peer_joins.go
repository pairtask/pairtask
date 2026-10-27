package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// peerJoinsSchema keeps this workspace's attempts to redeem another
// workspace's code (#178); see peer.JoinRecord. The code and exchange secrets
// are never stored.
const peerJoinsSchema = `
CREATE TABLE IF NOT EXISTS peer_joins (
    operation_id         TEXT PRIMARY KEY,
    joiner               TEXT NOT NULL,
    joiner_name          TEXT NOT NULL,
    invitation_id        TEXT NOT NULL DEFAULT '',
    inviter_operation_id TEXT NOT NULL DEFAULT '',
    peer                 TEXT NOT NULL DEFAULT '',
    transcript_id        BLOB,
    state                TEXT NOT NULL,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);
`

var _ peer.JoinStore = (*Store)(nil)

const joinColumns = `operation_id, joiner, joiner_name, invitation_id, inviter_operation_id, peer, transcript_id, state, created_at, updated_at`

// BeginJoin records a join's intent; see peer.JoinStore.
func (s *Store) BeginJoin(ctx context.Context, start peer.JoinStart) (peer.JoinRecord, error) {
	return s.inJoinTx(ctx, func(tx *sql.Tx) (peer.JoinRecord, error) {
		known, found, err := getJoin(ctx, tx, start.OperationID)
		if err != nil || found {
			return replayJoin(known, start, err)
		}
		counts, err := joinCounts(ctx, tx)
		if err != nil {
			return peer.JoinRecord{}, err
		}
		r, err := peer.NewJoin(start, counts)
		if err != nil {
			return peer.JoinRecord{}, err
		}
		return r, putJoin(ctx, tx, r)
	})
}

func replayJoin(known peer.JoinRecord, start peer.JoinStart, err error) (peer.JoinRecord, error) {
	if err != nil {
		return peer.JoinRecord{}, err
	}
	return peer.ReplayJoin(known, start)
}

func joinCounts(ctx context.Context, tx *sql.Tx) (peer.JoinCounts, error) {
	var c peer.JoinCounts
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE state NOT IN (?, ?)) FROM peer_joins`,
		string(peer.JoinFailed), string(peer.JoinUnverified)).Scan(&c.Total, &c.Open)
	return c, err
}

func (s *Store) SetCorrelation(ctx context.Context, opID string, h peer.InviterHello, at time.Time) (peer.JoinRecord, error) {
	return s.updateJoin(ctx, opID, func(r peer.JoinRecord) (peer.JoinRecord, bool, error) {
		return peer.Correlate(r, h, at)
	})
}

func (s *Store) RecordVerified(ctx context.Context, opID string, res peer.ExchangeResult, at time.Time) (peer.JoinRecord, error) {
	return s.updateJoin(ctx, opID, func(r peer.JoinRecord) (peer.JoinRecord, bool, error) {
		return peer.Verify(r, res, at)
	})
}

func (s *Store) RecordReceipt(ctx context.Context, opID string, receipt peer.JoinReceipt, at time.Time) (peer.JoinRecord, error) {
	return s.updateJoin(ctx, opID, func(r peer.JoinRecord) (peer.JoinRecord, bool, error) {
		return peer.Settle(r, receipt, at)
	})
}

func (s *Store) FailJoin(ctx context.Context, opID string, at time.Time) (peer.JoinRecord, error) {
	return s.updateJoin(ctx, opID, func(r peer.JoinRecord) (peer.JoinRecord, bool, error) {
		return peer.Fail(r, at)
	})
}

// RecoverJoins marks interrupted joins unverified; see peer.JoinStore.
func (s *Store) RecoverJoins(ctx context.Context, at time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE peer_joins SET state = ?, updated_at = ? WHERE state IN (?, ?)`,
		string(peer.JoinUnverified), stamp(at), string(peer.JoinIntent), string(peer.JoinExchanging))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (s *Store) GetJoin(ctx context.Context, opID string) (peer.JoinRecord, bool, error) {
	return getJoin(ctx, s.db, opID)
}

// Joins lists every join by operation ID.
func (s *Store) Joins(ctx context.Context) ([]peer.JoinRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+joinColumns+` FROM peer_joins ORDER BY operation_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []peer.JoinRecord{}
	for rows.Next() {
		r, _, err := scanJoin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// updateJoin applies a transition to a known join in one transaction.
func (s *Store) updateJoin(ctx context.Context, opID string, step func(peer.JoinRecord) (peer.JoinRecord, bool, error)) (peer.JoinRecord, error) {
	return s.inJoinTx(ctx, func(tx *sql.Tx) (peer.JoinRecord, error) {
		r, found, err := getJoin(ctx, tx, opID)
		if err != nil {
			return peer.JoinRecord{}, err
		}
		if !found {
			return peer.JoinRecord{}, peer.ErrNoJoin
		}
		next, changed, err := step(r)
		if err != nil || !changed {
			return next, err
		}
		return next, putJoin(ctx, tx, next)
	})
}

func (s *Store) inJoinTx(ctx context.Context, apply func(*sql.Tx) (peer.JoinRecord, error)) (peer.JoinRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return peer.JoinRecord{}, err
	}
	defer tx.Rollback()
	r, err := apply(tx)
	if err != nil {
		return peer.JoinRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return peer.JoinRecord{}, fmt.Errorf("commit join: %w", err)
	}
	return r, nil
}

func putJoin(ctx context.Context, tx *sql.Tx, r peer.JoinRecord) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO peer_joins (`+joinColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(operation_id) DO UPDATE SET invitation_id = excluded.invitation_id, inviter_operation_id = excluded.inviter_operation_id,
    peer = excluded.peer, transcript_id = excluded.transcript_id, state = excluded.state, updated_at = excluded.updated_at`,
		r.OperationID, r.Joiner, r.JoinerName, r.InvitationID, r.InviterOperationID, r.Peer, r.TranscriptID,
		string(r.State), stamp(r.CreatedAt), stamp(r.UpdatedAt))
	if err != nil {
		return fmt.Errorf("store join: %w", err)
	}
	return nil
}

func getJoin(ctx context.Context, q rowQuerier, opID string) (peer.JoinRecord, bool, error) {
	return scanJoin(q.QueryRowContext(ctx, `SELECT `+joinColumns+` FROM peer_joins WHERE operation_id = ?`, opID))
}

func scanJoin(row rowScanner) (peer.JoinRecord, bool, error) {
	var r peer.JoinRecord
	var state, created, updated string
	err := row.Scan(&r.OperationID, &r.Joiner, &r.JoinerName, &r.InvitationID, &r.InviterOperationID, &r.Peer,
		&r.TranscriptID, &state, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return peer.JoinRecord{}, false, nil
	}
	if err != nil {
		return peer.JoinRecord{}, false, err
	}
	r.State, r.TranscriptID = peer.JoinState(state), bytes.Clone(r.TranscriptID)
	if r.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return peer.JoinRecord{}, false, err
	}
	r.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return r, err == nil, err
}
