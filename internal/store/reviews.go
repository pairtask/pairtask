package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
)

// reviewsSchema keeps review requests (#201): one row per request, its view
// as JSON, with the columns the broker selects on. The token binds the
// current write intent and never leaves the broker.
const reviewsSchema = `
CREATE TABLE IF NOT EXISTS reviews (
    review_id  TEXT PRIMARY KEY,
    reviewer   TEXT NOT NULL,
    state      TEXT NOT NULL,
    hash       TEXT NOT NULL,
    token      TEXT NOT NULL DEFAULT '',
    doc        TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    expires_at TEXT
);
CREATE INDEX IF NOT EXISTS reviews_by_reviewer ON reviews(reviewer, state);
CREATE INDEX IF NOT EXISTS reviews_by_update ON reviews(updated_at);
CREATE INDEX IF NOT EXISTS reviews_by_expiry ON reviews(expires_at) WHERE expires_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS review_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    review_id   TEXT NOT NULL,
    at          TEXT NOT NULL,
    actor       TEXT NOT NULL,
    action      TEXT NOT NULL,
    state       TEXT NOT NULL,
    attempt     INTEGER NOT NULL,
    publication TEXT NOT NULL,
    intent      INTEGER NOT NULL,
    base        TEXT,
    head        TEXT,
    outcome     TEXT,
    receipt     TEXT,
    detail      TEXT
);
CREATE INDEX IF NOT EXISTS review_events_by_review ON review_events(review_id, id);
`

// ReviewAction names who changed a review and how, for its event trail; an
// operator's change also finishes its audit row (AuditID > 0) in the same
// transaction.
type ReviewAction struct {
	Actor, Action string
	AuditID       int64
	// Notice, on a new request, is the reviewer's durable message about it
	// (#220): stored with the review or not at all. A retry of a request
	// already stored writes no new one.
	Notice *ReviewNotice
}

// ReviewNotice is the message that tells a reviewer about a request, and
// who it is for.
type ReviewNotice struct {
	Msg        protocol.Message
	Recipients []protocol.Recipient
}

// openStates is the SQL list of states that still wait for or run a review.
const openStates = `('requested', 'ready', 'in_review')`

// CreateReview records a new request, or answers a retry of the same one
// with what is stored. The same ID with different content is refused, and
// so is a request beyond the open bounds. With an audit row (auditID > 0),
// its accepted result commits with the request, or nothing does.
func (s *Store) CreateReview(ctx context.Context, rec review.Record, act ReviewAction) (review.Record, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return review.Record{}, false, err
	}
	defer tx.Rollback()
	out, created, err := createReview(ctx, tx, rec)
	if err != nil {
		return review.Record{}, false, err
	}
	if err := notifyReviewer(ctx, tx, act.Notice, created); err != nil {
		return review.Record{}, false, err
	}
	if err := record(ctx, tx, reviewChange{out, created, act}); err != nil {
		return review.Record{}, false, err
	}
	return out, created, tx.Commit()
}

// notifyReviewer stores a new request's notice for its reviewer, durably
// and inside the request's transaction.
func notifyReviewer(ctx context.Context, tx *sql.Tx, notice *ReviewNotice, created bool) error {
	if notice == nil || !created {
		return nil
	}
	return persistMessage(ctx, tx, routedMessage{msg: notice.Msg, recipients: notice.Recipients})
}

func createReview(ctx context.Context, tx *sql.Tx, rec review.Record) (review.Record, bool, error) {
	known, found, err := loadReview(ctx, tx, rec.View.ID)
	switch {
	case err != nil:
		return review.Record{}, false, err
	case found && known.Hash == rec.Hash:
		return known, false, nil
	case found:
		return review.Record{}, false, review.ErrIDReused
	}
	if err := admitReview(ctx, tx, rec.View.Spec.Reviewer); err != nil {
		return review.Record{}, false, err
	}
	return rec, true, saveReview(ctx, tx, rec, true)
}

// reviewChange is one step's result and who took it.
type reviewChange struct {
	rec     review.Record
	changed bool
	act     ReviewAction
}

// record writes a change's event and finishes its operator audit row, both
// inside the change's own transaction.
func record(ctx context.Context, tx *sql.Tx, c reviewChange) error {
	if c.changed {
		if err := reviewEvent(ctx, tx, c.rec, c.act); err != nil {
			return err
		}
	}
	if c.act.AuditID <= 0 {
		return nil
	}
	return finishAudit(ctx, tx, c.act.AuditID, AuditOutcome{Result: AuditAccepted})
}

// reviewEvent is one change in a review's trail: who, what, and the state it
// left. Never the scope, the findings or any credential.
func reviewEvent(ctx context.Context, tx *sql.Tx, rec review.Record, act ReviewAction) error {
	v := rec.View
	var base, head, outcome, receipt any
	if v.Revisions != nil {
		base, head = v.Revisions.Base, v.Revisions.Head
	}
	if v.Verdict != nil {
		outcome = v.Verdict.Outcome
	}
	if v.Publication.Receipt != nil {
		receipt = v.Publication.Receipt.ReviewID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO review_events (review_id, at, actor, action, state, attempt, publication, intent, base, head, outcome, receipt, detail)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, v.ID, stamp(v.UpdatedAt), act.Actor, act.Action, v.State, v.Attempt, v.Publication.State,
		v.Publication.Intent, base, head, outcome, receipt, nullString(v.Publication.Reason))
	return err
}

// admitReview enforces the open bounds, in all and for one reviewer.
func admitReview(ctx context.Context, tx *sql.Tx, reviewer string) error {
	var total, theirs int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(reviewer = ?), 0) FROM reviews WHERE state IN `+openStates,
		reviewer).Scan(&total, &theirs)
	if err != nil {
		return err
	}
	if total >= protocol.ReviewsOpen || theirs >= protocol.ReviewsOpenPerAgent {
		return review.ErrFull
	}
	return nil
}

// ChangeReview applies one step to a stored review in a transaction; only a
// step that changed something is written. With an audit row (auditID > 0),
// its accepted result commits with the change, or nothing does.
func (s *Store) ChangeReview(ctx context.Context, id string, act ReviewAction, step func(review.Record) (review.Record, bool, error)) (review.Record, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return review.Record{}, false, err
	}
	defer tx.Rollback()
	rec, found, err := loadReview(ctx, tx, id)
	if err != nil {
		return review.Record{}, false, err
	}
	if !found {
		return review.Record{}, false, review.ErrNotFound
	}
	next, changed, err := step(rec)
	if err != nil {
		return next, false, err
	}
	if err := publicationAllowed(ctx, tx, rec, next); err != nil {
		return rec, false, err
	}
	if changed {
		if err := saveReview(ctx, tx, next, false); err != nil {
			return review.Record{}, false, err
		}
	}
	if err := record(ctx, tx, reviewChange{next, changed, act}); err != nil {
		return review.Record{}, false, err
	}
	return next, changed, tx.Commit()
}

// GetReview is one stored review.
func (s *Store) GetReview(ctx context.Context, id string) (review.Record, bool, error) {
	return loadReview(ctx, s.db, id)
}

// Reviews lists reviews newest first, those of one reviewer when reviewer is
// set, at most limit, with the total.
func (s *Store) Reviews(ctx context.Context, reviewer string, limit int) ([]review.Record, int, error) {
	where, args := ``, []any{}
	if reviewer != "" {
		where, args = ` WHERE reviewer = ?`, append(args, reviewer)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reviews`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+reviewColumns+` FROM reviews`+where+` ORDER BY updated_at DESC, review_id LIMIT ?`,
		append(args, limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []review.Record
	for rows.Next() {
		rec, err := scanReview(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, rec)
	}
	return out, total, rows.Err()
}

// ExpireReviews ends every open review whose time is up, and returns their IDs.
func (s *Store) ExpireReviews(ctx context.Context, now time.Time) ([]string, error) {
	due, err := s.dueReviews(ctx, now)
	if err != nil {
		return nil, err
	}
	var expired []string
	expire := func(r review.Record) (review.Record, bool, error) {
		next, changed := review.Expire(r, now)
		return next, changed, nil
	}
	for _, id := range due {
		_, changed, err := s.ChangeReview(ctx, id, ReviewAction{Actor: "broker", Action: "expire"}, expire)
		if err != nil {
			return expired, err
		}
		if changed {
			expired = append(expired, id)
		}
	}
	return expired, nil
}

// dueReviews are the open reviews whose time is up.
func (s *Store) dueReviews(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT review_id FROM reviews WHERE state IN `+openStates+` AND expires_at <= ?`, stamp(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var due []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		due = append(due, id)
	}
	return due, rows.Err()
}

const reviewColumns = `hash, token, doc`

func loadReview(ctx context.Context, q rowQuerier, id string) (review.Record, bool, error) {
	row := q.QueryRowContext(ctx, `SELECT `+reviewColumns+` FROM reviews WHERE review_id = ?`, id)
	rec, err := scanReview(row)
	if errors.Is(err, sql.ErrNoRows) {
		return review.Record{}, false, nil
	}
	return rec, err == nil, err
}

func scanReview(row interface{ Scan(...any) error }) (review.Record, error) {
	var rec review.Record
	var doc string
	if err := row.Scan(&rec.Hash, &rec.Token, &doc); err != nil {
		return rec, err
	}
	if err := json.Unmarshal([]byte(doc), &rec.View); err != nil {
		return rec, fmt.Errorf("review %w", err)
	}
	return rec, nil
}

func saveReview(ctx context.Context, tx *sql.Tx, rec review.Record, insert bool) error {
	doc, err := json.Marshal(rec.View)
	if err != nil {
		return err
	}
	v := rec.View
	var expires any
	if v.ExpiresAt != nil {
		expires = stamp(*v.ExpiresAt)
	}
	if insert {
		_, err = tx.ExecContext(ctx, `INSERT INTO reviews (review_id, reviewer, state, hash, token, doc, created_at, updated_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, v.ID, v.Spec.Reviewer, v.State, rec.Hash, rec.Token, string(doc), stamp(v.CreatedAt), stamp(v.UpdatedAt), expires)
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE reviews SET state = ?, token = ?, doc = ?, updated_at = ?, expires_at = ? WHERE review_id = ?`,
		v.State, rec.Token, string(doc), stamp(v.UpdatedAt), expires, v.ID)
	return err
}
