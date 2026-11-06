package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

const consentColumns = `cursor, id, contact, message_id, n, created_at, sender, recipients, reply_id, reply_author,
    text, review_ask, hash, targets, digest, state, revision, received_at, expires_at,
    COALESCE(choice, ''), COALESCE(audit_id, 0)`

// consentRow is one held request as stored; times stay as stored text,
// which compares in time order. A pruned request has no text, review or
// targets left: only what a replay is told apart by.
type consentRow struct {
	Cursor                 int64
	ID, Contact, MessageID string
	N                      int64
	CreatedAt, Sender      string
	Recipients             []string
	ReplyTo                messaging.ReplyRef
	Text                   string
	Review                 *messaging.ReviewAsk
	hash                   []byte
	Targets                []protocol.ConsentTarget
	Digest, State          string
	Revision               int64
	ReceivedAt, ExpiresAt  string
	Choice                 string
	AuditID                int64
}

func scanConsent(row rowScanner) (consentRow, error) {
	var c consentRow
	var recipients string
	var text, ask, targets sql.NullString
	err := row.Scan(&c.Cursor, &c.ID, &c.Contact, &c.MessageID, &c.N, &c.CreatedAt, &c.Sender, &recipients, &c.ReplyTo.ID, &c.ReplyTo.Author,
		&text, &ask, &c.hash, &targets, &c.Digest, &c.State, &c.Revision, &c.ReceivedAt, &c.ExpiresAt, &c.Choice, &c.AuditID)
	if err != nil {
		return c, err
	}
	c.Text = text.String
	if c.Review, err = scanReviewAsk(ask); err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(recipients), &c.Recipients); err != nil {
		return c, fmt.Errorf("consent recipients: %w", err)
	}
	return c, scanTargets(targets, &c.Targets)
}

// scanTargets reads a request's targets as stored, none once pruned.
func scanTargets(targets sql.NullString, out *[]protocol.ConsentTarget) error {
	if !targets.Valid {
		return nil
	}
	if err := json.Unmarshal([]byte(targets.String), out); err != nil {
		return fmt.Errorf("consent targets: %w", err)
	}
	return nil
}

func queryConsent(ctx context.Context, q rowsQuerier, where string, args ...any) ([]consentRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+consentColumns+` FROM consent_requests `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []consentRow
	for rows.Next() {
		c, err := scanConsent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// oneConsent is the held request a query names; found is false for none.
func oneConsent(row *sql.Row) (consentRow, bool, error) {
	c, err := scanConsent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return consentRow{}, false, nil
	}
	return c, err == nil, err
}

// consentByMessage is the request held under a message ID in the
// contact's current pairing (#276).
func consentByMessage(ctx context.Context, q rowQuerier, contact, messageID string) (consentRow, bool, error) {
	return oneConsent(q.QueryRowContext(ctx, `SELECT `+consentColumns+` FROM consent_requests
WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` AND message_id = ?2`, contact, messageID))
}

func consentByID(ctx context.Context, q rowQuerier, id string) (consentRow, bool, error) {
	return oneConsent(q.QueryRowContext(ctx, `SELECT `+consentColumns+` FROM consent_requests WHERE id = ?`, id))
}

// message is the request as it arrived.
func (c consentRow) message() (messaging.Message, error) {
	created, err := time.Parse(time.RFC3339Nano, c.CreatedAt)
	return messaging.Message{ID: c.MessageID, N: uint64(c.N), CreatedAt: created, Text: c.Text, From: c.Sender,
		To: c.Recipients, ReplyTo: c.ReplyTo, Review: c.Review}, err
}

// consentView is a held request as the operator decides it.
func consentView(ctx context.Context, q rowQuerier, c consentRow) (protocol.ConsentView, error) {
	label, err := optionalString(q.QueryRowContext(ctx, `SELECT name FROM peer_contacts WHERE identity_key = ?`, c.Contact))
	if err != nil {
		return protocol.ConsentView{}, err
	}
	v := protocol.ConsentView{ID: c.ID, Cursor: c.Cursor, Contact: c.Contact, WorkspaceKey: c.Contact, Label: label,
		ClaimedFrom: c.Sender, Targets: c.Targets, Review: viewAsk(c.Review), Text: c.Text, Digest: c.Digest,
		Revision: c.Revision, State: c.State}
	if v.ReceivedAt, err = time.Parse(time.RFC3339Nano, c.ReceivedAt); err != nil {
		return v, err
	}
	v.ExpiresAt, err = time.Parse(time.RFC3339Nano, c.ExpiresAt)
	return v, err
}

// viewAsk is a review request's pins and policy as shown.
func viewAsk(a *messaging.ReviewAsk) *protocol.ReviewAsk {
	if a == nil {
		return nil
	}
	return &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: a.Forge, Repository: a.Repository, Change: a.Change},
		Scope: a.Scope, Base: a.Base, Head: a.Head, Publish: a.Publish, Formal: a.Formal}
}
