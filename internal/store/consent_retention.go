package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// Held requests are retained like the messages they stand for (#293). A
// settled request whose outcome is answered keeps its payload until it is
// TombstoneAge old, then only its identity, sequence, hash and digest: a
// replay is still told apart, and never re-prompts. Those tombstones join
// the replay floor's run with the inbound rows, by sequence, and go when
// the floor passes them, so their sequences are then too old. One whose
// outcome isn't answered keeps everything. A request's identity counts
// toward the contact's remembered ones with the inbound rows, and is never
// dropped to make room.

// consentForgettable is a request the retention may act on at ?1 (the
// stamp TombstoneAge ago): settled, its outcome answered, and aged.
const consentForgettable = `state != 'pending' AND outcome IN ('answered', 'refused') AND decided_at < ?1`

// pruneConsentPayloads drops the text, review request and targets of the
// contact's forgettable requests; the rest of each row stays.
func pruneConsentPayloads(ctx context.Context, tx *sql.Tx, contact string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE consent_requests SET text = NULL, review_ask = NULL, targets = NULL
WHERE `+consentForgettable+` AND contact = ?2 AND text IS NOT NULL`, msgStamp(now.Add(-messaging.TombstoneAge)), contact)
	return err
}

// tombstoneKinds tell an inbound row from a held request in the floor's
// run; a row sorts before a request of the same sequence, so a retained
// message is never passed.
const (
	tombstoneRow     = 0
	tombstoneConsent = 1
)

// tombstone is one remembered identity in the floor's run: its row, its
// kind, and whether it may be forgotten.
type tombstone struct {
	cursor, n int64
	kind      int
	old       bool
}

// tombstonesQuery lists one pairing's (contact ?2, epoch ?3) remembered
// inbound identities by sequence: rows and held requests, each with
// whether it may be forgotten at ?1. Each pairing numbers its own (#276).
const tombstonesQuery = `SELECT cursor, n, kind, old FROM (
    SELECT cursor, n, 0 AS kind, stored_at < ?1 AND (state = 'read' AND receipt != 'pending'
        OR state = 'routed' AND NOT ` + pendingImport + ` OR state = 'refused' AND read_at IS NOT NULL) AND NOT (` + needsAck + `) AS old
    FROM peer_messages WHERE contact = ?2 AND pairing_epoch = ?3 AND direction = 'in'
    UNION ALL
    SELECT cursor, n, 1, ` + consentForgettable + ` FROM consent_requests WHERE contact = ?2 AND pairing_epoch = ?3
) ORDER BY n, kind`

// rememberedEpochs are the pairings of the contact with inbound identities
// still remembered.
func rememberedEpochs(ctx context.Context, tx *sql.Tx, contact string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT pairing_epoch FROM peer_messages WHERE contact = ?1 AND direction = 'in'
UNION SELECT pairing_epoch FROM consent_requests WHERE contact = ?1 ORDER BY 1`, contact)
	if err != nil {
		return nil, err
	}
	var out []int64
	for rows.Next() {
		var epoch int64
		if err := rows.Scan(&epoch); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, epoch)
	}
	return out, closeRows(rows)
}

// forgettableRun is the leading run of one pairing's remembered
// identities, by sequence, that may be forgotten; it stops at the first
// that must stay.
func forgettableRun(ctx context.Context, tx *sql.Tx, k pairingKey, now time.Time) ([]tombstone, error) {
	rows, err := tx.QueryContext(ctx, tombstonesQuery, msgStamp(now.Add(-messaging.TombstoneAge)), k.contact, k.epoch)
	if err != nil {
		return nil, err
	}
	var run []tombstone
	for rows.Next() {
		var t tombstone
		if err := rows.Scan(&t.cursor, &t.n, &t.kind, &t.old); err != nil {
			rows.Close()
			return nil, err
		}
		if !t.old {
			break
		}
		run = append(run, t)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	return run, nil
}

// forgetTombstone deletes one identity of the run.
func forgetTombstone(ctx context.Context, tx *sql.Tx, t tombstone) error {
	table := map[int]string{tombstoneRow: "peer_messages", tombstoneConsent: "consent_requests"}[t.kind]
	_, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE cursor = ?`, t.cursor)
	return err
}

// reclaimConsentRate drops the contact's arrival bucket once it would be
// full again: a partial one stays, so dropping it never refills it.
func reclaimConsentRate(ctx context.Context, tx *sql.Tx, contact string, now time.Time) error {
	b, found, err := readRateBucket(ctx, tx, contact)
	if err != nil || !found {
		return err
	}
	if !b.fullAt(now) {
		return nil
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM consent_rate WHERE contact = ?`, contact)
	return err
}

// fullAt: the bucket would be full again at now.
func (b rateBucket) fullAt(now time.Time) bool { return b.refilled(now).tokens >= ConsentBurst }

// rememberedIdentities is how many of the inbound identities of the
// contact's current pairing this side still remembers: its rows, and its
// held requests that have no row (an allowed one has the row it became).
// An earlier pairing's are history, never counted against this one (#276).
func rememberedIdentities(ctx context.Context, q rowQuerier, contact string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM peer_messages WHERE `+currentInbound+`)
    + (SELECT COUNT(*) FROM consent_requests cr WHERE cr.contact = ?1 AND cr.pairing_epoch = `+currentEpochOf1+` AND NOT EXISTS (
        SELECT 1 FROM peer_messages pm WHERE pm.contact = ?1 AND pm.pairing_epoch = cr.pairing_epoch AND pm.direction = 'in'
            AND pm.message_id = cr.message_id))`, contact).Scan(&n)
	return n, err
}

// identitiesFull: the remembered identities of the contact's current
// pairing are at their bound, so nothing new can be told from a replay.
func identitiesFull(ctx context.Context, q rowQuerier, contact string) (bool, error) {
	n, err := rememberedIdentities(ctx, q, contact)
	return n >= messaging.Tombstones, err
}
