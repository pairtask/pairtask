package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
)

// The outcomes of review requests a paired workspace sent here (#235),
// owed back to it. Each request admitted reserves its outcomes until its
// final one is answered; its outcomes are numbered, and only the latest
// not yet answered waits to be sent, so what waits is bounded by the
// requests admitted. Every outcome is written in its cause's transaction.

const reviewOutcomesSchema = `
CREATE TABLE IF NOT EXISTS review_outcome_seq (
    contact      TEXT NOT NULL,
    ask_id       TEXT NOT NULL,
    reviewer     TEXT NOT NULL,
    seq          INTEGER NOT NULL DEFAULT 0,
    terminal_seq INTEGER,
    acked_seq    INTEGER NOT NULL DEFAULT 0,
    refused      TEXT,
    created_at   TEXT NOT NULL,
    PRIMARY KEY (contact, ask_id)
);
CREATE TABLE IF NOT EXISTS review_outcomes_out (
    contact   TEXT NOT NULL,
    ask_id    TEXT NOT NULL,
    seq       INTEGER NOT NULL,
    payload   TEXT NOT NULL,
    queued_at TEXT NOT NULL,
    PRIMARY KEY (contact, ask_id)
);
`

// Bounds on requests whose outcomes are still owed.
const (
	OutcomesOwedPerContact = 256
	OutcomesOwedTotal      = 1024
)

// owed is a request whose final outcome isn't answered yet.
const owed = `refused IS NULL AND (terminal_seq IS NULL OR acked_seq < terminal_seq)`

// reserveOutcomes reserves a staged request's outcomes.
func reserveOutcomes(ctx context.Context, tx *sql.Tx, r newRow) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO review_outcome_seq (contact, ask_id, reviewer, created_at) VALUES (?, ?, ?, ?)
ON CONFLICT DO NOTHING`, r.key.contact, r.m.ID, r.m.To[0], msgStamp(r.now))
	return err
}

// outcomesFull: a contact's requests with outcomes owed, or all of them,
// are at their bound.
func outcomesFull(ctx context.Context, tx *sql.Tx, contact string) (bool, error) {
	var theirs, total int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(contact = ?), 0), COUNT(*) FROM review_outcome_seq WHERE `+owed, contact).Scan(&theirs, &total)
	return theirs >= OutcomesOwedPerContact || total >= OutcomesOwedTotal, err
}

// outcomeEvent is one outcome of a request, and when.
type outcomeEvent struct {
	contact, askID string
	o              messaging.ReviewOutcome
	terminal       bool
	at             time.Time
}

// emitOutcome numbers an outcome and makes it the one to send.
func emitOutcome(ctx context.Context, tx *sql.Tx, e outcomeEvent) error {
	var seq uint64
	err := tx.QueryRowContext(ctx, `UPDATE review_outcome_seq SET seq = seq + 1 WHERE contact = ? AND ask_id = ? RETURNING seq, reviewer`,
		e.contact, e.askID).Scan(&seq, &e.o.Reviewer)
	if err == sql.ErrNoRows {
		return nil // no request owed outcomes: nothing to send
	}
	if err != nil {
		return err
	}
	e.o.AskID, e.o.Seq = e.askID, seq
	if e.terminal {
		if _, err := tx.ExecContext(ctx, `UPDATE review_outcome_seq SET terminal_seq = ? WHERE contact = ? AND ask_id = ?`, seq, e.contact, e.askID); err != nil {
			return err
		}
	}
	payload, _ := json.Marshal(e.o) // a struct of strings and numbers always encodes
	_, err = tx.ExecContext(ctx, `INSERT INTO review_outcomes_out (contact, ask_id, seq, payload, queued_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (contact, ask_id) DO UPDATE SET seq = excluded.seq, payload = excluded.payload, queued_at = excluded.queued_at`,
		e.contact, e.askID, seq, string(payload), msgStamp(e.at))
	return err
}

// askOutcome is an outcome of a request before it is a review: its state,
// why, and when.
func askOutcome(ctx context.Context, tx *sql.Tx, imp RemoteImport, e askEnd) error {
	return emitOutcome(ctx, tx, outcomeEvent{contact: imp.Contact, askID: imp.RemoteID,
		o: messaging.ReviewOutcome{State: e.state, Reason: e.reason}, terminal: askTerminal[e.state], at: e.at})
}

// askTerminal are the outcomes that end a request before it is a review.
var askTerminal = map[string]bool{"declined": true, "expired": true, "withheld": true}

// PendingOutcomes is the oldest outcomes waiting to be sent to contact.
func (p *PeerMessages) PendingOutcomes(ctx context.Context, contact string, max int) ([]messaging.ReviewOutcome, error) {
	var payloads []string
	err := p.inTx(ctx, contact, func(tx *sql.Tx) (err error) {
		payloads, err = queryStrings(ctx, tx, `SELECT payload FROM review_outcomes_out WHERE contact = ? ORDER BY queued_at, rowid LIMIT ?`,
			contact, sessionBudget.clamp(budget{max, 0}).count)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]messaging.ReviewOutcome, 0, len(payloads))
	for _, data := range payloads {
		var o messaging.ReviewOutcome
		if err := json.Unmarshal([]byte(data), &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// OutcomeAnswered records contact's answer to an outcome: one no newer
// than the answer is no longer sent; Stored moves the answered mark up, and
// a refusal is kept with why, for the operator.
func (p *PeerMessages) OutcomeAnswered(ctx context.Context, contact string, r messaging.OutcomeReply) error {
	return p.inTx(ctx, contact, func(tx *sql.Tx) error {
		sent, err := queryOptional[uint64](ctx, tx, `SELECT seq FROM review_outcome_seq WHERE contact = ? AND ask_id = ?`, contact, r.AskID)
		if err != nil || sent == nil || r.Seq > *sent {
			return err // an answer to no outcome of ours: ignored
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM review_outcomes_out WHERE contact = ? AND ask_id = ? AND seq <= ?`, contact, r.AskID, r.Seq); err != nil {
			return err
		}
		mark, args := answerMark(contact, r)
		_, err = tx.ExecContext(ctx, mark, args...)
		return err
	})
}

// answerMark is how an answer marks its request's numbering: Stored moves
// the answered mark up, a refusal keeps why.
func answerMark(contact string, r messaging.OutcomeReply) (string, []any) {
	if r.Reason != "" {
		return `UPDATE review_outcome_seq SET refused = ?3 WHERE contact = ?1 AND ask_id = ?2`, []any{contact, r.AskID, r.Reason}
	}
	return `UPDATE review_outcome_seq SET acked_seq = MAX(acked_seq, ?3) WHERE contact = ?1 AND ask_id = ?2 AND seq >= ?3`,
		[]any{contact, r.AskID, r.Seq}
}

// queryOptional is the one value a query gives, or nil without a row.
func queryOptional[T any](ctx context.Context, q rowQuerier, query string, args ...any) (*T, error) {
	var v T
	err := q.QueryRowContext(ctx, query, args...).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// reviewOutcome is where a review a paired workspace asked for stands, as
// an outcome, and whether that is final.
func reviewOutcome(v protocol.ReviewView) (messaging.ReviewOutcome, bool) {
	o := messaging.ReviewOutcome{Attempt: uint32(v.Attempt), Intent: uint32(v.Publication.Intent), Reason: v.Reason}
	if v.Verdict != nil {
		o.Verdict, o.Summary = v.Verdict.Outcome, v.Verdict.Summary
	}
	if r := v.Publication.Receipt; r != nil {
		o.URL = r.URL
	}
	o.State = reviewOutcomeState(v)
	o.Summary, o.URL, o.Reason = clip(o.Summary, messaging.OutcomeSummaryBytes), clip(o.URL, messaging.OutcomeFieldBytes), clip(o.Reason, messaging.OutcomeFieldBytes)
	return o, reviewOutcomeTerminal[o.State]
}

// clip keeps at most max bytes of s, never splitting a character.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// reviewOutcomeStates are a review's own states as outcomes; a completed
// one is told by its publication.
var reviewOutcomeStates = map[string]string{
	protocol.ReviewRequested: "requested", protocol.ReviewReady: "requested", protocol.ReviewInReview: "accepted",
	protocol.ReviewDeclined: "declined", protocol.ReviewExpired: "review_expired", protocol.ReviewCancelled: "cancelled",
}

// publicationOutcomes are a completed review's publication states as outcomes.
var publicationOutcomes = map[string]string{
	protocol.PublicationNone: "completed", protocol.PublicationPending: "submitted", protocol.PublicationSubmitting: "publishing",
	protocol.PublicationPublished: "published", protocol.PublicationFailed: "publication_failed",
	protocol.PublicationUncertain: "uncertain", protocol.PublicationStale: "stale", protocol.PublicationCancelled: "cancelled",
}

// reviewOutcomeTerminal are the outcomes after which nothing more comes,
// but a cancelled review's in-flight write being reconciled.
var reviewOutcomeTerminal = map[string]bool{"declined": true, "review_expired": true, "cancelled": true,
	"completed": true, "published": true, "stale": true}

func reviewOutcomeState(v protocol.ReviewView) string {
	if v.State == protocol.ReviewCompleted || v.Publication.State == protocol.PublicationPublished {
		return publicationOutcomes[v.Publication.State]
	}
	return reviewOutcomeStates[v.State]
}

// reviewChanged emits a review's outcome when a change moved what its
// requester sees: its state, attempt, intent or verdict.
func reviewChanged(ctx context.Context, tx *sql.Tx, before, after review.Record) error {
	v := after.View
	if v.Remote == nil || v.RemoteRequest == "" {
		return nil
	}
	was, _ := reviewOutcome(before.View)
	now, terminal := reviewOutcome(v)
	if was == now {
		return nil
	}
	return emitOutcome(ctx, tx, outcomeEvent{contact: v.Remote.Contact, askID: v.RemoteRequest, o: now, terminal: terminal, at: v.UpdatedAt})
}

// pruneOutcomes forgets the outcome numbering of requests whose outcomes
// are all answered, or refused, once their import is forgotten too.
func pruneOutcomes(ctx context.Context, tx *sql.Tx, contact string, _ time.Time) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM review_outcome_seq WHERE contact = ?1 AND NOT (`+owed+`)
    AND NOT EXISTS (SELECT 1 FROM remote_imports ri WHERE ri.contact = ?1 AND ri.remote_id = review_outcome_seq.ask_id)
    AND NOT EXISTS (SELECT 1 FROM review_outcomes_out o WHERE o.contact = ?1 AND o.ask_id = review_outcome_seq.ask_id)`, contact)
	return err
}
