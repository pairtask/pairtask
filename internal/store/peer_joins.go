package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
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

const joinInsertColumns = `operation_id, joiner, joiner_name, invitation_id, inviter_operation_id, peer, transcript_id, state, created_at, updated_at, destination, decision_confirmed_at, repair, begun_mark`

// joinSelect reads joins, with the mode of their ledger row (#276).
// destination came later: older rows have NULL, read as empty.
const joinSelect = `SELECT j.operation_id, j.joiner, j.joiner_name, j.invitation_id, j.inviter_operation_id, j.peer, j.transcript_id,
    j.state, j.created_at, j.updated_at, COALESCE(j.destination, ''), COALESCE(j.decision_confirmed_at, ''), j.repair, j.begun_mark,
    COALESCE(j.retired_at, ''), COALESCE(x.mode, '')
FROM peer_joins j LEFT JOIN pairing_exchanges x ON x.operation_id = j.operation_id AND x.role = 'joiner'`

// BeginJoin records a join's intent, with the authority mark it begins
// under, read in the same transaction; see peer.JoinStore.
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
		mark, err := readMark(ctx, tx)
		if err != nil {
			return peer.JoinRecord{}, err
		}
		r.BegunMark = uint64(mark)
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
	// The open states are exactly peer.JoinState.Open's.
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE state IN (?, ?, ?, ?)) FROM peer_joins`,
		string(peer.JoinIntent), string(peer.JoinExchanging), string(peer.JoinOutcomeUnknown),
		string(peer.JoinAwaitingAcceptance)).Scan(&c.Total, &c.Open)
	return c, err
}

func (s *Store) SetCorrelation(ctx context.Context, opID string, h peer.InviterHello, at time.Time) (peer.JoinRecord, error) {
	return s.updateJoin(ctx, opID, func(r peer.JoinRecord) (peer.JoinRecord, bool, error) {
		return peer.Correlate(r, h, at)
	})
}

// RecordVerified records the authenticated inviter; an epoch-capable
// exchange writes its ledger row first, and a refusal keeps only that row
// (#276). See peer.JoinStore.
func (s *Store) RecordVerified(ctx context.Context, opID string, res peer.ExchangeResult, at time.Time) (peer.JoinRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return peer.JoinRecord{}, err
	}
	defer tx.Rollback()
	r, err := verifyJoin(ctx, tx, verified{opID, res, at})
	if err = commitKept(tx, err); err != nil {
		return peer.JoinRecord{}, err
	}
	return r, nil
}

func (s *Store) RecordReceipt(ctx context.Context, opID string, receipt peer.JoinReceipt, at time.Time) (peer.JoinRecord, error) {
	return s.updateJoin(ctx, opID, func(r peer.JoinRecord) (peer.JoinRecord, bool, error) {
		return peer.ApplyReceipt(r, receipt, at)
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
	rows, err := s.db.QueryContext(ctx, joinSelect+` ORDER BY j.operation_id`)
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
	_, err := tx.ExecContext(ctx, `INSERT INTO peer_joins (`+joinInsertColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(operation_id) DO UPDATE SET invitation_id = excluded.invitation_id, inviter_operation_id = excluded.inviter_operation_id,
    peer = excluded.peer, transcript_id = excluded.transcript_id, state = excluded.state, updated_at = excluded.updated_at,
    decision_confirmed_at = excluded.decision_confirmed_at`,
		r.OperationID, r.Joiner, r.JoinerName, r.InvitationID, r.InviterOperationID, r.Peer, r.TranscriptID,
		string(r.State), stamp(r.CreatedAt), stamp(r.UpdatedAt), r.Destination, optionalStamp(r.DecisionConfirmedAt), r.Repair, int64(r.BegunMark))
	if err != nil {
		return fmt.Errorf("store join: %w", err)
	}
	return nil
}

func getJoin(ctx context.Context, q rowQuerier, opID string) (peer.JoinRecord, bool, error) {
	return scanJoin(q.QueryRowContext(ctx, joinSelect+` WHERE j.operation_id = ?`, opID))
}

func scanJoin(row rowScanner) (peer.JoinRecord, bool, error) {
	var r peer.JoinRecord
	var state, mode string
	var mark int64
	times := make([]string, 4)
	err := row.Scan(&r.OperationID, &r.Joiner, &r.JoinerName, &r.InvitationID, &r.InviterOperationID, &r.Peer,
		&r.TranscriptID, &state, &times[0], &times[1], &r.Destination, &times[2], &r.Repair, &mark, &times[3], &mode)
	if errors.Is(err, sql.ErrNoRows) {
		return peer.JoinRecord{}, false, nil
	}
	if err != nil {
		return peer.JoinRecord{}, false, err
	}
	r.State, r.TranscriptID, r.BegunMark = peer.JoinState(state), bytes.Clone(r.TranscriptID), uint64(mark)
	if mode == string(peer.ModeEpoch) {
		r.Mode = peer.ModeEpoch
	}
	if err := scanTimes(times[2:], &r.DecisionConfirmedAt, &r.RetiredAt); err != nil {
		return peer.JoinRecord{}, false, err
	}
	if r.CreatedAt, err = time.Parse(time.RFC3339Nano, times[0]); err != nil {
		return peer.JoinRecord{}, false, err
	}
	r.UpdatedAt, err = time.Parse(time.RFC3339Nano, times[1])
	return r, err == nil, err
}

// RecordDecision records the inviter's decision on a join awaiting it; an
// acceptance of a legacy join makes the contact in the same transaction,
// while an epoch-capable one installs only at its confirmation. A retired
// join takes no decision. See peer.JoinStore.
func (s *Store) RecordDecision(ctx context.Context, opID string, decision peer.PairingState, at time.Time) (peer.JoinRecord, error) {
	return s.inJoinTx(ctx, func(tx *sql.Tx) (peer.JoinRecord, error) {
		r, err := liveJoin(ctx, tx, opID)
		if err != nil {
			return peer.JoinRecord{}, err
		}
		next, changed, err := peer.Decide(r, decision, at)
		if err != nil || !changed {
			return next, err
		}
		if err := decideJoin(ctx, tx, next, decision); err != nil {
			return peer.JoinRecord{}, err
		}
		return next, putJoin(ctx, tx, next)
	})
}

// knownJoin is the join, or peer.ErrNoJoin.
func knownJoin(ctx context.Context, tx *sql.Tx, opID string) (peer.JoinRecord, error) {
	r, found, err := getJoin(ctx, tx, opID)
	if err == nil && !found {
		err = peer.ErrNoJoin
	}
	return r, err
}

// liveJoin is a known join no unpair retired.
func liveJoin(ctx context.Context, tx *sql.Tx, opID string) (peer.JoinRecord, error) {
	r, err := knownJoin(ctx, tx, opID)
	if err == nil && !r.RetiredAt.IsZero() {
		err = peer.ErrJoinRetired
	}
	if err != nil {
		return peer.JoinRecord{}, err
	}
	return r, nil
}

// decideJoin records a decision where it has authority: on the ledger row
// of an epoch-capable join, else as the legacy contact an acceptance makes.
func decideJoin(ctx context.Context, tx *sql.Tx, next peer.JoinRecord, decision peer.PairingState) error {
	if next.Mode.Epoch() {
		return decideExchange(ctx, tx, exchangeDecision{next.OperationID, decision, next.UpdatedAt})
	}
	if next.State != peer.JoinContact {
		return nil
	}
	return upsertContact(ctx, tx, peer.JoinerContact(next), next.OperationID)
}

// RecordDecisionConfirmed records, once, that the inviter confirmed the
// decision; for an epoch-capable join it delivers the exchange, which
// installs an acceptance (#276). A retired join is peer.ErrJoinRetired.
func (s *Store) RecordDecisionConfirmed(ctx context.Context, opID string, decision peer.PairingState, at time.Time) (peer.JoinRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return peer.JoinRecord{}, err
	}
	defer tx.Rollback()
	r, err := confirmJoin(ctx, tx, confirmation{opID, decision, at})
	if err = commitKept(tx, err); err != nil {
		return peer.JoinRecord{}, err
	}
	return r, nil
}

// confirmation is the inviter's confirmation of one join's decision.
type confirmation struct {
	opID     string
	decision peer.PairingState
	at       time.Time
}

// confirmJoin is RecordDecisionConfirmed inside its transaction: the
// exchange's delivery replays or installs first, then the join records it.
func confirmJoin(ctx context.Context, tx *sql.Tx, c confirmation) (peer.JoinRecord, error) {
	r, err := liveJoin(ctx, tx, c.opID)
	if err != nil {
		return peer.JoinRecord{}, err
	}
	next, changed, err := peer.ConfirmDecision(r, c.decision, c.at)
	if err != nil {
		return peer.JoinRecord{}, err
	}
	if err := deliverJoin(ctx, tx, next, c); err != nil {
		return peer.JoinRecord{}, err
	}
	if !changed {
		return next, nil
	}
	return next, putJoin(ctx, tx, next)
}

// deliverJoin delivers an epoch-capable join's exchange.
func deliverJoin(ctx context.Context, tx *sql.Tx, next peer.JoinRecord, c confirmation) error {
	if !next.Mode.Epoch() {
		return nil
	}
	x, found, err := exchangeRow(ctx, tx, next.OperationID)
	if err != nil || !found {
		return err
	}
	_, err = settleExchange(ctx, tx, installing{x: x, decision: c.decision, at: c.at, contact: peer.JoinerContact(next)})
	return err
}

// verified is the exchange a join authenticated, and when.
type verified struct {
	opID string
	res  peer.ExchangeResult
	at   time.Time
}

// verifyJoin is RecordVerified inside its transaction: a replay of a
// committed exchange answers it, a legacy one records as before, an
// epoch-capable one commits its ledger row first.
func verifyJoin(ctx context.Context, tx *sql.Tx, v verified) (peer.JoinRecord, error) {
	r, err := knownJoin(ctx, tx, v.opID)
	if err != nil {
		return peer.JoinRecord{}, err
	}
	x, committed, err := exchangeRow(ctx, tx, v.opID)
	if err != nil {
		return peer.JoinRecord{}, err
	}
	if committed {
		return replayVerified(r, x, v)
	}
	next, changed, err := peer.Verify(r, v.res, v.at)
	if err != nil || !changed {
		return next, err
	}
	if v.res.Mode.Epoch() {
		return verifyEpoch(ctx, tx, next)
	}
	return next, putJoin(ctx, tx, next)
}

// replayVerified answers a join whose exchange the ledger holds: the same
// exchange answers the join, or its refusal; another conflicts.
func replayVerified(r peer.JoinRecord, x peer.ExchangeRecord, v verified) (peer.JoinRecord, error) {
	same := []bool{x.IdentityKey == v.res.Peer, bytes.Equal(x.TranscriptID, v.res.TranscriptID), x.Mode == v.res.Mode.Stored()}
	if slices.Contains(same, false) {
		return peer.JoinRecord{}, peer.ErrJoinConflict
	}
	if x.State == peer.ExchangeRefused {
		if err := peer.RefusalError(x.Reason); err != nil {
			return peer.JoinRecord{}, err
		}
	}
	return r, nil
}

// verifyEpoch commits the join's ledger row, then the join; an address
// move merges the contact's new destination now, as it never installs.
func verifyEpoch(ctx context.Context, tx *sql.Tx, next peer.JoinRecord) (peer.JoinRecord, error) {
	x, err := commitExchange(ctx, tx, joinExchange(next))
	if err != nil {
		return peer.JoinRecord{}, err
	}
	next.Mode = peer.ModeEpoch
	if x.Kind == peer.KindAddressMove {
		if err := upsertContact(ctx, tx, peer.JoinerContact(next), next.OperationID); err != nil {
			return peer.JoinRecord{}, err
		}
	}
	return next, putJoin(ctx, tx, next)
}
