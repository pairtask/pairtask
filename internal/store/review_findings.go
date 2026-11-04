package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Findings a request's outcomes carried (#303): one snapshot per request,
// the findings of the newest verdict told, bound to the outcome that
// carried them. A later outcome of the same attempt without findings, or
// with the same ones, leaves the snapshot and where it came from alone;
// one of another attempt without findings clears it, so old findings never
// look current; the same outcome again may add findings it lacked once,
// and never change ones it had.

// findingsSnapshot is a request's findings as kept: nil bytes for none
// known, and the outcome they are bound to.
type findingsSnapshot struct {
	envelope []byte
	attempt  sql.NullInt64
}

func loadFindings(ctx context.Context, tx *sql.Tx, a AskOut) (findingsSnapshot, error) {
	var s findingsSnapshot
	var envelope sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT findings, findings_attempt FROM review_asks_out WHERE contact = ? AND ask_id = ?`,
		a.Contact, a.AskID).Scan(&envelope, &s.attempt)
	if envelope.Valid {
		s.envelope = []byte(envelope.String)
	}
	return s, err
}

// sameAttempt: the snapshot came with a verdict of this attempt.
func (s findingsSnapshot) sameAttempt(attempt uint32) bool {
	return s.attempt.Valid && uint32(s.attempt.Int64) == attempt
}

// keepFindings applies an outcome's findings to its request's snapshot,
// as judged: an applied outcome may replace or clear it, the same outcome
// again may only add what it lacked. Findings that contradict the ones
// the same outcome brought make it a conflict.
func keepFindings(ctx context.Context, tx *sql.Tx, a AskOut, o messaging.ReviewOutcome, verdict outcomeVerdict) (outcomeVerdict, error) {
	if !bringsFindings(verdict, a, o) {
		return verdict, nil // older, invalid or conflicting: never findings
	}
	envelope, err := messaging.EncodeFindings(o.Findings)
	if err != nil {
		return outcomeInvalid, nil
	}
	s, err := loadFindings(ctx, tx, a)
	if err != nil {
		return verdict, err
	}
	if verdict == outcomeStale {
		if s.contradicts(envelope) {
			return outcomeConflict, nil
		}
		return verdict, s.enrich(ctx, tx, findingsWrite{a, o, envelope})
	}
	return verdict, s.advance(ctx, tx, findingsWrite{a, o, envelope})
}

// bringsFindings: an outcome that may change its request's findings, a
// newer one applied or the current one again.
func bringsFindings(verdict outcomeVerdict, a AskOut, o messaging.ReviewOutcome) bool {
	return verdict == outcomeApplied || verdict == outcomeStale && o.Seq == a.Seq
}

// findingsWrite is an outcome's findings for its request: envelope nil
// for none.
type findingsWrite struct {
	a        AskOut
	o        messaging.ReviewOutcome
	envelope []byte
}

// advance takes a newer outcome's findings: any it brings replace the
// snapshot, bound to it; none from the same attempt leave the snapshot as
// it came, none from another clear it.
func (s findingsSnapshot) advance(ctx context.Context, tx *sql.Tx, w findingsWrite) error {
	if w.envelope == nil && s.sameAttempt(w.o.Attempt) {
		return nil
	}
	return writeFindings(ctx, tx, w)
}

// contradicts: the current outcome again, with other findings than the
// ones known. A known snapshot is always the current attempt's: another
// attempt without findings clears it.
func (s findingsSnapshot) contradicts(envelope []byte) bool {
	return envelope != nil && s.envelope != nil && !bytes.Equal(envelope, s.envelope)
}

// enrich takes the current outcome again: findings are added once, only
// while none are known.
func (s findingsSnapshot) enrich(ctx context.Context, tx *sql.Tx, w findingsWrite) error {
	if w.envelope == nil || s.envelope != nil {
		return nil
	}
	return writeFindings(ctx, tx, w)
}

func writeFindings(ctx context.Context, tx *sql.Tx, w findingsWrite) error {
	var envelope any
	if w.envelope != nil {
		envelope = string(w.envelope)
	}
	digest := w.o.Digest()
	_, err := tx.ExecContext(ctx, `UPDATE review_asks_out SET findings = ?, findings_seq = ?, findings_attempt = ?, findings_intent = ?,
    findings_digest = ? WHERE contact = ? AND ask_id = ?`, envelope, w.o.Seq, w.o.Attempt, w.o.Intent, digest[:], w.a.Contact, w.a.AskID)
	return err
}

// noticeFindings is how many findings a notice shows.
const noticeFindings = 3

// findingsText is what a notice says of the findings an outcome brought:
// the first few, then how many more were received, apart from how many
// stayed on the reviewer's side; "" for none.
func findingsText(e *messaging.FindingsEnvelope) string {
	if e == nil || e.Total == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "; findings (%d):", e.Total)
	for _, f := range e.Items[:min(noticeFindings, len(e.Items))] {
		b.WriteString(" " + findingLine(f) + ";")
	}
	if more := len(e.Items) - noticeFindings; more > 0 {
		fmt.Fprintf(&b, " %d more received: reviews_asked;", more)
	}
	if kept := e.Total - len(e.Items); kept > 0 {
		fmt.Fprintf(&b, " %d not transferred, kept on their side;", kept)
	}
	return strings.TrimSuffix(b.String(), ";")
}

// findingLine is a finding on one line: where, then what, controls as
// spaces.
func findingLine(f messaging.Finding) string {
	where := f.Path
	if f.Line > 0 {
		where += ":" + strconv.Itoa(f.Line)
	}
	line := where + ": " + f.Summary
	if f.Truncated {
		line += "…"
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, line)
}

// keptFindings are a request's findings columns as read.
type keptFindings struct {
	envelope     sql.NullString
	seq, attempt sql.NullInt64
}

// view is the findings as the asked list shows them, nil for none.
func (k keptFindings) view() *protocol.AskFindings {
	if !k.envelope.Valid {
		return nil
	}
	e, err := messaging.DecodeFindings([]byte(k.envelope.String))
	if err != nil || e == nil {
		return nil // only a valid envelope is ever kept
	}
	out := &protocol.AskFindings{Total: e.Total, Items: make([]protocol.AskFinding, len(e.Items)), Seq: uint64(k.seq.Int64), Attempt: uint32(k.attempt.Int64)}
	for i, f := range e.Items {
		out.Items[i] = protocol.AskFinding{Path: f.Path, Line: f.Line, Summary: f.Summary, Truncated: f.Truncated}
	}
	return out
}
