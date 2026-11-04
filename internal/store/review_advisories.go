package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Review advisories (#304): what this side knows that suggests a review
// request to a contact's agent may be refused, read where the request is
// queued, before it is written. Advice only: the request goes regardless,
// and only the other side decides.

// ReviewAdvisories are the advisories for asking reviewer at contact.
func ReviewAdvisories(ctx context.Context, q rowQuerier, contact, reviewer string) ([]protocol.ReviewAdvisory, error) {
	var out []protocol.ReviewAdvisory
	for _, read := range []func(context.Context, rowQuerier, string, string) (*protocol.ReviewAdvisory, error){notInDirectory, refusedNotShared} {
		a, err := read(ctx, q, contact, reviewer)
		if err != nil {
			return nil, err
		}
		if a != nil {
			out = append(out, *a)
		}
	}
	return out, nil
}

// notInDirectory: the contact said what it shares, and not this agent.
// Nothing is said while it never has.
func notInDirectory(ctx context.Context, q rowQuerier, contact, reviewer string) (*protocol.ReviewAdvisory, error) {
	var seen sql.NullString
	var listed bool
	err := q.QueryRowContext(ctx, `SELECT directory_at, EXISTS (SELECT 1 FROM peer_directory WHERE contact = ?1 AND agent = ?2)
FROM peer_contacts WHERE identity_key = ?1`, contact, reviewer).Scan(&seen, &listed)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (!seen.Valid || listed) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return advisoryAt(protocol.AdvisoryNotInDirectory, seen.String, "")
}

// refusedNotShared: the newest request to this agent there was refused
// not_shared, and no outcome from there has moved a request to it on
// since. A request merely queued here clears nothing.
func refusedNotShared(ctx context.Context, q rowQuerier, contact, reviewer string) (*protocol.ReviewAdvisory, error) {
	var askID, at string
	err := q.QueryRowContext(ctx, `SELECT r.ask_id, r.updated_at FROM review_asks_out r
WHERE r.contact = ?1 AND r.reviewer = ?2 AND r.state = 'refused' AND r.reason = 'not_shared'
  AND NOT EXISTS (SELECT 1 FROM review_asks_out n WHERE n.contact = ?1 AND n.reviewer = ?2 AND n.seq > 0 AND n.updated_at > r.updated_at)
ORDER BY r.updated_at DESC LIMIT 1`, contact, reviewer).Scan(&askID, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return advisoryAt(protocol.AdvisoryRefusedNotShared, at, askID)
}

func advisoryAt(code, stamp, askID string) (*protocol.ReviewAdvisory, error) {
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return nil, err
	}
	return &protocol.ReviewAdvisory{Code: code, At: &at, AskID: askID}, nil
}
