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
// here; and this side's requests to paired workspaces not yet settled, in
// each contact's current pairing (#276).
func (s *Store) ReviewCounts(ctx context.Context) (protocol.ReviewCounts, error) {
	var c protocol.ReviewCounts
	// One read, so what failed and what is in progress never disagree.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	if c.AwaitingYou, c.AwaitingToken, err = awaitingSet(ctx, tx); err != nil {
		return c, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM reviews WHERE state IN `+openStates).Scan(&c.Running); err != nil {
		return c, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_asks_out WHERE settled_at IS NULL
    AND pairing_epoch = `+currentEpochFor("review_asks_out.contact")).Scan(&c.AskedOpen); err != nil {
		return c, err
	}
	return c, failureCounts(ctx, tx, &c)
}

// failureCounts adds what failed and the asks still in progress (#298).
func failureCounts(ctx context.Context, tx *sql.Tx, c *protocol.ReviewCounts) error {
	var err error
	if c.Failed, c.FailedToken, err = failures(ctx, tx, allPairings); err != nil {
		return err
	}
	active, err := activeAsks(ctx, tx)
	c.AskedActive = &active
	return err
}

// awaitingSet counts the requests awaiting the operator and hashes which
// they are: "" for none.
func awaitingSet(ctx context.Context, q rowsQuerier) (int, string, error) {
	keys, err := queryStrings(ctx, q, `SELECT contact || char(0) || remote_id FROM remote_imports
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
	Kind    string `json:"k,omitempty"` // a failure's kind (#298)
	At      string `json:"t"`
	Contact string `json:"c"`
	// Epoch is a request's pairing (#276), a tiebreak between requests
	// of one contact asked at the same time; a cursor without one, from
	// before epochs were listed, reads as the first pairing's.
	Epoch int64  `json:"e,omitempty"`
	ID    string `json:"i"`
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
	if err != nil {
		return nil, ErrReviewCursor
	}
	var c pendingCursor
	if json.Unmarshal(data, &c) != nil || c.At == "" {
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
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	// The set's token is read with the page, so a client marks seen only
	// the set this page showed.
	if page.Total, page.Token, err = awaitingSet(ctx, tx); err != nil {
		return page, err
	}
	where, args := awaitingWhere, []any{}
	if cur != nil {
		where += afterAwaiting
		args = append(args, sql.Named("t", cur.At), sql.Named("c", cur.Contact), sql.Named("i", cur.ID))
	}
	rows, err := tx.QueryContext(ctx, `SELECT ri.contact, ri.remote_id, COALESCE(pm.cursor, 0), ri.sender, pm.review_ask, ri.staged_at
FROM remote_imports ri LEFT JOIN peer_messages pm ON pm.contact = ri.contact AND pm.pairing_epoch = ri.pairing_epoch
    AND pm.direction = 'in' AND pm.message_id = ri.remote_id
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

// askedOrder is open first, then newest change, then by contact, pairing
// (#276) and ID; afterAsked is everything past a cursor in it.
const (
	askedOrder = `ORDER BY (a.settled_at IS NOT NULL), a.updated_at DESC, a.contact, a.pairing_epoch, a.ask_id`
	afterAsked = `((a.settled_at IS NOT NULL) > :s OR (a.settled_at IS NOT NULL) = :s AND (a.updated_at < :t
    OR a.updated_at = :t AND (a.contact > :c OR a.contact = :c AND (a.pairing_epoch > :e OR a.pairing_epoch = :e AND a.ask_id > :i))))`
)

// askedColumns are a request as the asked list shows it, with its message's
// cursor and, when the contact held it for its operator (#293), where that
// hold stands; askedFrom joins them, each in the request's own pairing
// (#276).
const (
	askedColumns = `a.contact, a.ask_id, COALESCE(pm.cursor, 0), a.reviewer, a.state, a.verdict, a.url,
    a.updated_at, a.settled_at IS NOT NULL, COALESCE(a.review_id, ''), a.reason, a.findings, a.findings_seq, a.findings_attempt,
    COALESCE(ch.state, ''), COALESCE(ch.reason, ''), a.pairing_epoch`
	askedFrom = `review_asks_out a LEFT JOIN peer_messages pm ON pm.contact = a.contact AND pm.pairing_epoch = a.pairing_epoch
    AND pm.direction = 'out' AND pm.message_id = a.ask_id
    LEFT JOIN consent_held_out ch ON ch.contact = a.contact AND ch.pairing_epoch = a.pairing_epoch AND ch.message_id = a.ask_id`
)

// askedRow is one request as the asked list reads it: the item, its
// stored change time, its pairing, and its findings as kept.
type askedRow struct {
	item    protocol.ReviewAskedItem
	updated string
	epoch   int64
	f       keptFindings
}

// scanAsked reads one request of askedColumns, with at most limit
// findings.
func scanAsked(row rowScanner, limit int) (askedRow, error) {
	var r askedRow
	item := &r.item
	err := row.Scan(&item.Contact, &item.AskID, &item.Cursor, &item.Reviewer, &item.State, &item.Verdict, &item.URL,
		&r.updated, &item.Settled, &item.ReviewID, &item.Reason, &r.f.envelope, &r.f.seq, &r.f.attempt, &item.Consent, &item.ConsentReason,
		&r.epoch)
	if err != nil {
		return r, err
	}
	item.Findings, item.PairingEpoch = r.f.view(limit), r.epoch
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, r.updated)
	return r, err
}

// cursor is the asked list's position at this request.
func (r askedRow) cursor() string {
	return pendingCursor{Settled: r.item.Settled, At: r.updated, Contact: r.item.Contact, Epoch: r.epoch, ID: r.item.AskID}.encode()
}

// AskedReviews is one page of this side's requests to paired workspaces,
// after the cursor, under every pairing (#276).
func (s *Store) AskedReviews(ctx context.Context, before string) (protocol.ReviewAskedPage, error) {
	return s.askedReviews(ctx, before, allPairings)
}

// CurrentAskedReviews is AskedReviews in the newest pairing with each
// contact its history holds (#276): what an operator without pairing
// history reads, its total and cursors over that alone.
func (s *Store) CurrentAskedReviews(ctx context.Context, before string) (protocol.ReviewAskedPage, error) {
	return s.askedReviews(ctx, before, currentPairings)
}

// askedReviews is one page of the requests in scope, after the cursor.
func (s *Store) askedReviews(ctx context.Context, before string, scope pairingScope) (protocol.ReviewAskedPage, error) {
	page := protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{}}
	cur, err := decodeCursor(before)
	if err != nil {
		return page, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_asks_out a`+scope.where()).Scan(&page.Total); err != nil {
		return page, err
	}
	where, args := askedAfter(cur, scope)
	rows, err := s.db.QueryContext(ctx, `SELECT `+askedColumns+` FROM `+askedFrom+where+` `+askedOrder+` LIMIT `+pendingLimit, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	var cursors []string
	for rows.Next() {
		r, err := scanAsked(rows, 0) // a list carries counts; one request alone, its findings
		if err != nil {
			return page, err
		}
		page.Items, cursors = append(page.Items, r.item), append(cursors, r.cursor())
	}
	page.More, page.Next, err = finishPage(rows, &page.Items, cursors)
	return page, err
}

// askedAfter is the condition on the requests in scope past a cursor, nil
// for the first page, with its arguments.
func askedAfter(cur *pendingCursor, scope pairingScope) (string, []any) {
	if cur == nil {
		return scope.where(), nil
	}
	return scope.where(afterAsked), []any{sql.Named("s", cur.Settled), sql.Named("t", cur.At), sql.Named("c", cur.Contact),
		sql.Named("e", max(cur.Epoch, 1)), sql.Named("i", cur.ID)}
}

// AskedByRefs are exactly the requests the refs name, in their order, each
// once: those not kept are left out (#305).
func (s *Store) AskedByRefs(ctx context.Context, refs []protocol.ReviewAskRef) (protocol.ReviewAskedPage, error) {
	page := protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	seen := map[protocol.ReviewAskRef]bool{}
	for _, ref := range refs {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		item, found, err := askedItem(ctx, tx, ref, protocol.FindingsPreview)
		if err != nil {
			return page, err
		}
		if found {
			page.Items = append(page.Items, item)
		}
	}
	page.Total = len(page.Items)
	return page, nil
}

// AskedDetail is one asked request with every finding received (#303).
func (s *Store) AskedDetail(ctx context.Context, ref protocol.ReviewAskRef) (protocol.ReviewAskedPage, error) {
	page := protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	item, found, err := askedItem(ctx, tx, ref, protocol.FindingsAll)
	if found {
		page.Items, page.Total = append(page.Items, item), 1
	}
	return page, err
}

// askedItem is one request, of the pairing the ref names, 0 for the
// contact's current one (#276), as the asked list shows it, with at most
// limit findings.
func askedItem(ctx context.Context, tx *sql.Tx, ref protocol.ReviewAskRef, limit int) (protocol.ReviewAskedItem, bool, error) {
	epoch, args := refEpoch(ref)
	r, err := scanAsked(tx.QueryRowContext(ctx, `SELECT `+askedColumns+` FROM `+askedFrom+`
WHERE a.contact = ?1 AND a.pairing_epoch = `+epoch+` AND a.ask_id = ?2`, args...), limit)
	if err == sql.ErrNoRows {
		return r.item, false, nil
	}
	return r.item, err == nil, err
}

// refEpoch is the pairing a ref names as a condition on contact ?1, with
// the ref's arguments: its epoch as ?3, or the contact's current one.
func refEpoch(ref protocol.ReviewAskRef) (string, []any) {
	if ref.PairingEpoch == 0 {
		return currentEpochOf1, []any{ref.Contact, ref.AskID}
	}
	return "?3", []any{ref.Contact, ref.AskID, ref.PairingEpoch}
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
