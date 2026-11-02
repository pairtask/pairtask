package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Pending reviews for the operator (#286): counted over every row, so the
// counts never depend on what a page held, and paged by an opaque cursor
// over a total order, so a page never skips or repeats an item.

// ErrReviewCursor is a review_before that names no page.
var ErrReviewCursor = errors.New("review_before is not a cursor this broker gave")

// ReviewCounts are the reviews pending: requests from paired workspaces
// waiting for this operator, with a token naming that set; reviews running
// here; and this side's requests to paired workspaces not yet settled.
func (s *Store) ReviewCounts(ctx context.Context) (protocol.ReviewCounts, error) {
	var c protocol.ReviewCounts
	var err error
	if c.AwaitingYou, c.AwaitingToken, err = s.awaitingSet(ctx); err != nil {
		return c, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reviews WHERE state IN `+openStates).Scan(&c.Running); err != nil {
		return c, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_asks_out WHERE settled_at IS NULL`).Scan(&c.AskedOpen)
	return c, err
}

// awaitingSet counts the requests awaiting the operator and hashes which
// they are: "" for none.
func (s *Store) awaitingSet(ctx context.Context) (int, string, error) {
	keys, err := queryStrings(ctx, s.db, `SELECT contact || char(0) || remote_id FROM remote_imports
WHERE kind = 'review' AND state = 'awaiting' ORDER BY contact, remote_id`)
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

// pendingCursor is a page's position in its order, opaque to the client.
type pendingCursor struct {
	Settled bool   `json:"s,omitempty"`
	At      string `json:"t"`
	Contact string `json:"c"`
	ID      string `json:"i"`
}

func (c pendingCursor) encode() string {
	data, _ := json.Marshal(c) // strings and a bool always encode
	return base64.RawURLEncoding.EncodeToString(data)
}

// decodeCursor reads review_before: none for "".
func decodeCursor(s string) (*pendingCursor, error) {
	if s == "" {
		return nil, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(s)
	var c pendingCursor
	if err != nil || json.Unmarshal(data, &c) != nil || c.At == "" {
		return nil, ErrReviewCursor
	}
	return &c, nil
}

// awaitingOrder is newest first, then by contact and ID; afterAwaiting is
// everything past a cursor in it.
const (
	awaitingOrder = `ORDER BY ri.staged_at DESC, ri.contact, ri.remote_id`
	afterAwaiting = ` AND (ri.staged_at < :t OR ri.staged_at = :t AND (ri.contact > :c OR ri.contact = :c AND ri.remote_id > :i))`
	awaitingWhere = `ri.kind = 'review' AND ri.state = 'awaiting'`
)

// AwaitingReviews is one page of the requests awaiting the operator, after
// the cursor.
func (s *Store) AwaitingReviews(ctx context.Context, before string) (protocol.ReviewAwaitingPage, error) {
	page := protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{}}
	cur, err := decodeCursor(before)
	if err != nil {
		return page, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM remote_imports ri WHERE `+awaitingWhere).Scan(&page.Total); err != nil {
		return page, err
	}
	where, args := awaitingWhere, []any{}
	if cur != nil {
		where += afterAwaiting
		args = append(args, sql.Named("t", cur.At), sql.Named("c", cur.Contact), sql.Named("i", cur.ID))
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ri.contact, ri.remote_id, COALESCE(pm.cursor, 0), ri.sender, pm.review_ask, ri.staged_at
FROM remote_imports ri LEFT JOIN peer_messages pm ON pm.contact = ri.contact AND pm.direction = 'in' AND pm.message_id = ri.remote_id
WHERE `+where+` `+awaitingOrder+` LIMIT `+pendingLimit, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	var cursors []string
	for rows.Next() {
		item, at, err := scanAwaiting(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
		cursors = append(cursors, pendingCursor{At: at, Contact: item.Contact, ID: item.MessageID}.encode())
	}
	page.More, page.Next, err = finishPage(rows, &page.Items, cursors)
	return page, err
}

func scanAwaiting(rows *sql.Rows) (protocol.ReviewAwaitingItem, string, error) {
	var item protocol.ReviewAwaitingItem
	var ask sql.NullString
	var staged string
	if err := rows.Scan(&item.Contact, &item.MessageID, &item.Cursor, &item.Sender, &ask, &staged); err != nil {
		return item, "", err
	}
	a, err := scanReviewAsk(ask)
	if err != nil {
		return item, "", err
	}
	item.Ask = AskView(a)
	item.ReceivedAt, err = time.Parse(time.RFC3339Nano, staged)
	return item, staged, err
}

// AskView is a stored review request as the operator sees it, nil for none.
func AskView(a *messaging.ReviewAsk) *protocol.ReviewAsk {
	if a == nil {
		return nil
	}
	return &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: a.Forge, Repository: a.Repository, Change: a.Change},
		Scope: a.Scope, Base: a.Base, Head: a.Head, Publish: a.Publish, Formal: a.Formal}
}

// askedOrder is open first, then newest change, then by contact and ID;
// afterAsked is everything past a cursor in it.
const (
	askedOrder = `ORDER BY (a.settled_at IS NOT NULL), a.updated_at DESC, a.contact, a.ask_id`
	afterAsked = ` WHERE ((a.settled_at IS NOT NULL) > :s OR (a.settled_at IS NOT NULL) = :s AND (a.updated_at < :t
    OR a.updated_at = :t AND (a.contact > :c OR a.contact = :c AND a.ask_id > :i)))`
)

// AskedReviews is one page of this side's requests to paired workspaces,
// after the cursor.
func (s *Store) AskedReviews(ctx context.Context, before string) (protocol.ReviewAskedPage, error) {
	page := protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{}}
	cur, err := decodeCursor(before)
	if err != nil {
		return page, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_asks_out`).Scan(&page.Total); err != nil {
		return page, err
	}
	where, args := "", []any{}
	if cur != nil {
		where = afterAsked
		args = append(args, sql.Named("s", cur.Settled), sql.Named("t", cur.At), sql.Named("c", cur.Contact), sql.Named("i", cur.ID))
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.contact, a.ask_id, COALESCE(pm.cursor, 0), a.reviewer, a.state, a.verdict, a.url,
    a.updated_at, a.settled_at IS NOT NULL
FROM review_asks_out a LEFT JOIN peer_messages pm ON pm.contact = a.contact AND pm.direction = 'out' AND pm.message_id = a.ask_id
`+where+` `+askedOrder+` LIMIT `+pendingLimit, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	var cursors []string
	for rows.Next() {
		var item protocol.ReviewAskedItem
		var updated string
		if err := rows.Scan(&item.Contact, &item.AskID, &item.Cursor, &item.Reviewer, &item.State, &item.Verdict, &item.URL,
			&updated, &item.Settled); err != nil {
			return page, err
		}
		if item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
		cursors = append(cursors, pendingCursor{Settled: item.Settled, At: updated, Contact: item.Contact, ID: item.AskID}.encode())
	}
	page.More, page.Next, err = finishPage(rows, &page.Items, cursors)
	return page, err
}

// pendingLimit reads one item past a page, to tell whether more follow.
var pendingLimit = strconv.Itoa(protocol.ReviewPendingPage + 1)

// finishPage drops the extra item read past a full page: more follow, and
// the next page starts after the last item kept, never the one dropped.
func finishPage[T any](rows *sql.Rows, items *[]T, cursors []string) (bool, string, error) {
	if err := rows.Err(); err != nil {
		return false, "", err
	}
	if len(*items) <= protocol.ReviewPendingPage {
		return false, "", nil
	}
	*items = (*items)[:protocol.ReviewPendingPage]
	return true, cursors[protocol.ReviewPendingPage-1], nil
}
