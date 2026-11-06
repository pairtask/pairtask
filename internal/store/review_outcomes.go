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

// reviewOutcomeSeqColumns is review_outcome_seq with pairing_epoch in its key (#276).
const reviewOutcomeSeqColumns = `(
    contact      TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL DEFAULT 1,
    ask_id       TEXT NOT NULL,
    reviewer     TEXT NOT NULL,
    seq          INTEGER NOT NULL DEFAULT 0,
    terminal_seq INTEGER,
    acked_seq    INTEGER NOT NULL DEFAULT 0,
    refused      TEXT,
    created_at   TEXT NOT NULL,
    PRIMARY KEY (contact, pairing_epoch, ask_id)
)`

// reviewOutcomesOutColumns is review_outcomes_out with pairing_epoch in its key (#276).
const reviewOutcomesOutColumns = `(
    contact   TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL DEFAULT 1,
    ask_id    TEXT NOT NULL,
    seq       INTEGER NOT NULL,
    payload   TEXT NOT NULL,
    queued_at TEXT NOT NULL,
    PRIMARY KEY (contact, pairing_epoch, ask_id)
)`

// Bounds on requests whose outcomes are still owed.
const (
	OutcomesOwedPerContact = 256
	OutcomesOwedTotal      = 1024
)

// owed is a request whose final outcome isn't answered yet.
const owed = `refused IS NULL AND (terminal_seq IS NULL OR acked_seq < terminal_seq)`

// reserveOutcomes reserves a staged request's outcomes, in the pairing it
// came under: its contact's current one.
func reserveOutcomes(ctx context.Context, tx *sql.Tx, r newRow) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO review_outcome_seq (contact, pairing_epoch, ask_id, reviewer, created_at)
VALUES (?, `+currentEpoch+`, ?, ?, ?) ON CONFLICT DO NOTHING`, r.key.contact, r.key.contact, r.m.ID, r.m.To[0], msgStamp(r.now))
	return err
}

// outcomesFull: the requests of a contact's current pairing with outcomes
// owed, or all contacts' current ones, are at their bound (#276).
func outcomesFull(ctx context.Context, tx *sql.Tx, contact string) (bool, error) {
	var theirs, total int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(contact = ?), 0), COUNT(*) FROM review_outcome_seq
WHERE `+owed+` AND pairing_epoch = `+currentEpochFor("review_outcome_seq.contact"), contact).Scan(&theirs, &total)
	return theirs >= OutcomesOwedPerContact || total >= OutcomesOwedTotal, err
}

// outcomeEvent is one outcome of a request, the pairing it came under,
// and when.
type outcomeEvent struct {
	src      pairingKey
	askID    string
	o        messaging.ReviewOutcome
	terminal bool
	at       time.Time
}

// emitOutcome numbers an outcome and makes it the one to send, in the
// pairing its request came under; nothing for one that pairing ended.
func emitOutcome(ctx context.Context, tx *sql.Tx, e outcomeEvent) error {
	retired, err := outcomeContactRetired(ctx, tx, e.src)
	if err != nil || retired {
		return err
	}
	numbered, err := numberOutcome(ctx, tx, &e)
	if err != nil || !numbered {
		return err
	}
	payload, _ := json.Marshal(e.o) // a struct of strings and numbers always encodes
	_, err = tx.ExecContext(ctx, `INSERT INTO review_outcomes_out (contact, pairing_epoch, ask_id, seq, payload, queued_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (contact, pairing_epoch, ask_id) DO UPDATE SET seq = excluded.seq, payload = excluded.payload, queued_at = excluded.queued_at`,
		e.src.contact, e.src.epoch, e.askID, e.o.Seq, string(payload), msgStamp(e.at))
	return err
}

// numberOutcome gives the outcome its request's next number, and marks a
// final one; numbered is false for a request owing no outcomes.
func numberOutcome(ctx context.Context, tx *sql.Tx, e *outcomeEvent) (bool, error) {
	err := tx.QueryRowContext(ctx, `UPDATE review_outcome_seq SET seq = seq + 1,
    terminal_seq = CASE WHEN ?4 THEN seq + 1 ELSE terminal_seq END
WHERE contact = ?1 AND pairing_epoch = ?2 AND ask_id = ?3 AND refused IS NULL RETURNING seq, reviewer`,
		e.src.contact, e.src.epoch, e.askID, e.terminal).Scan(&e.o.Seq, &e.o.Reviewer)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // no request owed outcomes: nothing to send
	}
	e.o.AskID = e.askID
	return err == nil, err
}

// outcomeContactRetired lets local work finish without producing traffic
// for a pairing this operator ended, or one since replaced by another.
// Paused pairings still queue outcomes.
func outcomeContactRetired(ctx context.Context, tx *sql.Tx, src pairingKey) (bool, error) {
	err := refuseRetired(ctx, tx, src)
	if errors.Is(err, ErrUnpaired) {
		return true, nil
	}
	return false, err
}

// askOutcome is an outcome of a request before it is a review: its state,
// why, and when, in the pairing its import was staged under (#276).
func askOutcome(ctx context.Context, tx *sql.Tx, imp RemoteImport, e askEnd) error {
	return emitOutcome(ctx, tx, outcomeEvent{src: imp.source(), askID: imp.RemoteID,
		o: messaging.ReviewOutcome{State: e.state, Reason: e.reason}, terminal: askTerminal[e.state], at: e.at})
}

// askTerminal are the outcomes that end a request before it is a review.
var askTerminal = map[string]bool{"declined": true, "expired": true, "withheld": true}

// PendingOutcomes is the oldest outcomes of contact's current pairing
// waiting to be sent to it, each naming the review its request became
// here, if it became one (#259): read as it is sent, never stored with the
// outcome, so the outcome and its digest are the same either way.
func (p *PeerMessages) PendingOutcomes(ctx context.Context, contact string, max int) ([]messaging.ReviewOutcome, error) {
	var out []messaging.ReviewOutcome
	err := p.inTx(ctx, contact, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT o.payload, `+linkedReview+` FROM review_outcomes_out o
WHERE o.contact = ? AND o.pairing_epoch = `+currentEpoch+` ORDER BY o.queued_at, o.rowid LIMIT ?`,
			contact, contact, sessionBudget.clamp(budget{max, 0}).count)
		if err != nil {
			return err
		}
		out, err = scanOutcomes(rows)
		return err
	})
	return out, err
}

// linkedReview is the review an outcome's request became here: the one
// that names its contact and request, for its reviewer, in the outcome's
// pairing; "" for none, as before it is a review.
const linkedReview = `COALESCE((SELECT r.review_id FROM reviews r JOIN review_outcome_seq s
    ON s.contact = o.contact AND s.pairing_epoch = o.pairing_epoch AND s.ask_id = o.ask_id
    WHERE r.reviewer = s.reviewer AND json_extract(r.doc, '$.remote.contact') = o.contact AND json_extract(r.doc, '$.remote_request') = o.ask_id
    AND COALESCE(r.remote_epoch, 1) = o.pairing_epoch
    ORDER BY r.created_at, r.review_id LIMIT 1), '')`

func scanOutcomes(rows *sql.Rows) ([]messaging.ReviewOutcome, error) {
	defer rows.Close()
	var out []messaging.ReviewOutcome
	for rows.Next() {
		var data, reviewID string
		if err := rows.Scan(&data, &reviewID); err != nil {
			return nil, err
		}
		var o messaging.ReviewOutcome
		if err := json.Unmarshal([]byte(data), &o); err != nil {
			return nil, err
		}
		o.ReviewID = sendableID(reviewID)
		out = append(out, o)
	}
	return out, rows.Err()
}

// sendableID is a review ID as an outcome may carry it, "" for one it
// can't.
func sendableID(id string) string {
	if protocol.ValidateReviewID(id) != nil {
		return ""
	}
	return id
}

// OutcomeAnswered records contact's answer to an outcome of its current
// pairing: one no newer than the answer is no longer sent; Stored moves
// the answered mark up, and a refusal is kept with why, for the operator.
// An earlier pairing's outcomes and numbering are never touched.
func (p *PeerMessages) OutcomeAnswered(ctx context.Context, contact string, r messaging.OutcomeReply) error {
	return p.inTx(ctx, contact, func(tx *sql.Tx) error {
		epoch, err := contactEpoch(ctx, tx, contact)
		if err != nil {
			return err
		}
		return answerOutcome(ctx, tx, pairingKey{contact, epoch}, r)
	})
}

// answerOutcome records an answer to an outcome of the pairing k.
func answerOutcome(ctx context.Context, tx *sql.Tx, k pairingKey, r messaging.OutcomeReply) error {
	sent, err := queryOptional[uint64](ctx, tx, `SELECT seq FROM review_outcome_seq WHERE contact = ? AND pairing_epoch = ? AND ask_id = ?`,
		k.contact, k.epoch, r.AskID)
	if err != nil || !answersSent(sent, r) {
		return err // an answer to no outcome of ours: ignored
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM review_outcomes_out WHERE contact = ? AND pairing_epoch = ? AND ask_id = ? AND seq <= ?`,
		k.contact, k.epoch, r.AskID, r.Seq); err != nil {
		return err
	}
	mark, args := answerMark(k, r)
	_, err = tx.ExecContext(ctx, mark, args...)
	return err
}

// answersSent: the answer is to an outcome this side sent.
func answersSent(sent *uint64, r messaging.OutcomeReply) bool {
	return sent != nil && r.Seq <= *sent
}

// answerMark is how an answer marks its request's numbering: Stored moves
// the answered mark up, a refusal keeps why.
func answerMark(k pairingKey, r messaging.OutcomeReply) (string, []any) {
	if r.Reason != "" {
		return `UPDATE review_outcome_seq SET refused = ?3 WHERE contact = ?1 AND pairing_epoch = ?4 AND ask_id = ?2`,
			[]any{k.contact, r.AskID, r.Reason, k.epoch}
	}
	return `UPDATE review_outcome_seq SET acked_seq = MAX(acked_seq, ?3) WHERE contact = ?1 AND pairing_epoch = ?4 AND ask_id = ?2 AND seq >= ?3`,
		[]any{k.contact, r.AskID, r.Seq, k.epoch}
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
	now.Findings = verdictFindings(v.Verdict) // projected once, kept with the outcome (#303)
	epoch, err := remoteEpoch(ctx, tx, v.ID)
	if err != nil {
		return err
	}
	return emitOutcome(ctx, tx, outcomeEvent{src: pairingKey{v.Remote.Contact, epoch}, askID: v.RemoteRequest, o: now, terminal: terminal, at: v.UpdatedAt})
}

// verdictFindings is what an outcome carries of a verdict's findings, nil
// without a verdict.
func verdictFindings(v *protocol.Verdict) *messaging.FindingsEnvelope {
	if v == nil {
		return nil
	}
	full := make([]messaging.Finding, len(v.Findings))
	for i, f := range v.Findings {
		full[i] = messaging.Finding{Path: f.Path, Line: f.Line, Summary: f.Summary}
	}
	return messaging.ProjectFindings(len(v.Findings), full)
}

// pruneOutcomes forgets the outcome numbering of requests whose outcomes
// are all answered, or refused, once their import is forgotten too, each
// pairing's by its own import and outcomes: another pairing's import of
// the same ask ID never keeps it (#276).
func pruneOutcomes(ctx context.Context, tx *sql.Tx, contact string, _ time.Time) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM review_outcome_seq WHERE contact = ?1 AND NOT (`+owed+`)
    AND NOT EXISTS (SELECT 1 FROM remote_imports ri WHERE ri.contact = ?1 AND ri.pairing_epoch = review_outcome_seq.pairing_epoch
        AND ri.remote_id = review_outcome_seq.ask_id)
    AND NOT EXISTS (SELECT 1 FROM review_outcomes_out o WHERE o.contact = ?1 AND o.pairing_epoch = review_outcome_seq.pairing_epoch
        AND o.ask_id = review_outcome_seq.ask_id)`, contact)
	return err
}
