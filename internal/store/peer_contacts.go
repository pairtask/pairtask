package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// peerContactsSchema keeps one row per paired workspace (#178); see
// peer.Contact. Rows are written only with the decision that makes them.
const peerContactsSchema = `
CREATE TABLE IF NOT EXISTS peer_contacts (
    identity_key   TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    destination    TEXT NOT NULL,
    established_at TEXT NOT NULL,
    joined         INTEGER NOT NULL,
    invited        INTEGER NOT NULL,
    local_name     TEXT NOT NULL DEFAULT '',
    destination_at TEXT NOT NULL DEFAULT ''
);
`

var _ peer.ContactStore = (*Store)(nil)

const contactColumns = `identity_key, name, destination, established_at, joined, invited, local_name, destination_at`

// upsertContact merges one direction into an identity's contact, inside the
// caller's transaction, refusing a new identity to a full table; opID is the
// pairing operation it comes from, named in the audit of a move.
func upsertContact(ctx context.Context, tx *sql.Tx, c peer.Contact, opID string) error {
	existing, found, err := getContact(ctx, tx, c.IdentityKey)
	if err != nil {
		return err
	}
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM peer_contacts`).Scan(&total); err != nil {
		return err
	}
	if err := peer.AdmitContact(found, total); err != nil {
		return err
	}
	merged := peer.MergeContact(existing, found, c)
	// local_name is the operator's alone: a merge never writes it.
	_, err = tx.ExecContext(ctx, `INSERT INTO peer_contacts (`+contactColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(identity_key) DO UPDATE SET name = excluded.name, destination = excluded.destination,
    joined = excluded.joined, invited = excluded.invited, destination_at = excluded.destination_at`,
		merged.IdentityKey, merged.Name, merged.Destination, stamp(merged.EstablishedAt), merged.Joined, merged.Invited, merged.LocalName,
		destinationStamp(merged.DestinationAt))
	if err != nil {
		return fmt.Errorf("store contact: %w", err)
	}
	return auditMove(ctx, tx, contactMove{existing, merged, opID})
}

// contactMove is a contact before and after a merge, and the pairing
// operation that merged it.
type contactMove struct {
	before, after peer.Contact
	opID          string
}

// auditMove records a known contact's move to another destination.
func auditMove(ctx context.Context, tx *sql.Tx, m contactMove) error {
	if m.before.Destination == "" || m.before.Destination == m.after.Destination {
		return nil
	}
	return insertAudit(ctx, tx, OperatorAudit{Action: "contact destination", MessageID: m.opID, Scope: m.after.IdentityKey,
		Result: AuditAccepted, Detail: m.before.Destination + " -> " + m.after.Destination})
}

// destinationStamp is a destination's time as stored: "" before any.
func destinationStamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return stamp(t)
}

func getContact(ctx context.Context, q rowQuerier, identityKey string) (peer.Contact, bool, error) {
	return scanContact(q.QueryRowContext(ctx, `SELECT `+contactColumns+` FROM peer_contacts WHERE identity_key = ?`, identityKey))
}

func scanContact(row rowScanner) (peer.Contact, bool, error) {
	var c peer.Contact
	var established, destinationAt string
	err := row.Scan(&c.IdentityKey, &c.Name, &c.Destination, &established, &c.Joined, &c.Invited, &c.LocalName, &destinationAt)
	if errors.Is(err, sql.ErrNoRows) {
		return peer.Contact{}, false, nil
	}
	if err != nil {
		return peer.Contact{}, false, err
	}
	if c.EstablishedAt, err = time.Parse(time.RFC3339Nano, established); err != nil {
		return c, false, err
	}
	if destinationAt != "" {
		c.DestinationAt, err = time.Parse(time.RFC3339Nano, destinationAt)
	}
	return c, err == nil, err
}

// Contact is one identity's contact, if any.
func (s *Store) Contact(ctx context.Context, identityKey string) (peer.Contact, bool, error) {
	return getContact(ctx, s.db, identityKey)
}

// Contacts lists every contact by identity key.
func (s *Store) Contacts(ctx context.Context) ([]peer.Contact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+contactColumns+` FROM peer_contacts ORDER BY identity_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []peer.Contact{}
	for rows.Next() {
		c, _, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
