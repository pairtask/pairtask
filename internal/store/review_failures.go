package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// What failed among reviews (#298): reviews here whose publication failed
// or is uncertain, and this side's requests to paired workspaces that
// failed, settled or not. Each failure is named by what it stands as, so a
// retry, a new attempt or a new outcome names a new failure. Nothing here
// ages a failure out: it lasts as long as its review or request is kept.

// failedAskStates are the requests that failed: a publication failed or
// uncertain there, still open, or settled without a review.
const failedAskStates = `('publication_failed', 'uncertain', 'refused', 'unsent', 'expired', 'review_expired', 'contact_removed')`

// failedSet is every failure, one row each: its kind, contact and ID, its
// state, what it reviews, when it last changed, and the key naming it as
// it stands. The key holds when it last changed too, so a set with the
// same token is in the same order, page after page.
const failedSet = `SELECT 'ask' AS kind, a.contact AS contact, a.ask_id AS id, COALESCE(pm.cursor, 0) AS cursor, a.state AS state,
    a.reviewer AS reviewer, COALESCE(json_extract(a.ask, '$.Repository'), '') AS repository,
    COALESCE(json_extract(a.ask, '$.Change'), '') AS change, a.reason AS reason, a.updated_at AS updated_at,
    'ask' || char(0) || a.contact || char(0) || a.ask_id || char(0) || a.seq || char(0) || a.state || char(0) || a.updated_at AS fkey
FROM review_asks_out a LEFT JOIN peer_messages pm ON pm.contact = a.contact AND pm.direction = 'out' AND pm.message_id = a.ask_id
WHERE a.state IN ` + failedAskStates + `
UNION ALL
SELECT 'review', '', r.review_id, 0, json_extract(r.doc, '$.publication.state'), r.reviewer,
    COALESCE(json_extract(r.doc, '$.spec.repository'), ''), COALESCE(json_extract(r.doc, '$.spec.change'), ''),
    COALESCE(json_extract(r.doc, '$.publication.reason'), ''), r.updated_at,
    'review' || char(0) || r.review_id || char(0) || COALESCE(json_extract(r.doc, '$.attempt'), 0) || char(0)
        || COALESCE(json_extract(r.doc, '$.publication.intent'), 0) || char(0) || json_extract(r.doc, '$.publication.state')
        || char(0) || r.updated_at
FROM reviews r WHERE json_extract(r.doc, '$.publication.state') IN ('failed', 'uncertain')`

// failedOrder is newest change first, then by kind, contact and ID;
// afterFailed is everything past a cursor in it.
const (
	failedOrder = `ORDER BY updated_at DESC, kind, contact, id`
	afterFailed = ` WHERE (updated_at < :t OR updated_at = :t AND (kind > :k OR kind = :k AND (contact > :c OR contact = :c AND id > :i)))`
)

// failures counts the failures and hashes which they are, as they stand:
// "" for none.
func failures(ctx context.Context, q rowsQuerier) (int, string, error) {
	keys, err := queryStrings(ctx, q, `SELECT fkey FROM (`+failedSet+`) ORDER BY fkey`)
	if err != nil || len(keys) == 0 {
		return 0, "", err
	}
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
	}
	return len(keys), hex.EncodeToString(h.Sum(nil))[:32], nil
}

// activeAsks counts this side's open requests still in progress: open,
// and not failed.
func activeAsks(ctx context.Context, q rowQuerier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_asks_out WHERE settled_at IS NULL AND state NOT IN `+failedAskStates).Scan(&n)
	return n, err
}

// FailedReviews is one page of the failures, after the cursor, read in one
// transaction with the set's total and token.
func (s *Store) FailedReviews(ctx context.Context, before string) (protocol.ReviewFailedPage, error) {
	page := protocol.ReviewFailedPage{Items: []protocol.ReviewFailedItem{}}
	cur, err := decodeCursor(before)
	if err != nil {
		return page, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if page.Total, page.Token, err = failures(ctx, tx); err != nil {
		return page, err
	}
	where, args := "", []any{}
	if cur != nil {
		where = afterFailed
		args = append(args, sql.Named("t", cur.At), sql.Named("k", cur.Kind), sql.Named("c", cur.Contact), sql.Named("i", cur.ID))
	}
	rows, err := tx.QueryContext(ctx, `SELECT kind, contact, id, cursor, state, reviewer, repository, change, reason, updated_at
FROM (`+failedSet+`)`+where+` `+failedOrder+` LIMIT `+pendingLimit, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	var cursors []string
	for rows.Next() {
		item, at, err := scanFailed(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
		cursors = append(cursors, pendingCursor{At: at, Kind: item.Kind, Contact: item.Contact, ID: item.ID}.encode())
	}
	page.More, page.Next, err = finishPage(rows, &page.Items, cursors)
	return page, err
}

func scanFailed(rows *sql.Rows) (protocol.ReviewFailedItem, string, error) {
	var item protocol.ReviewFailedItem
	var updated string
	if err := rows.Scan(&item.Kind, &item.Contact, &item.ID, &item.Cursor, &item.State, &item.Reviewer, &item.Repository,
		&item.Change, &item.Reason, &updated); err != nil {
		return item, "", err
	}
	at, err := time.Parse(time.RFC3339Nano, updated)
	item.UpdatedAt = at.UTC()
	return item, updated, err
}
