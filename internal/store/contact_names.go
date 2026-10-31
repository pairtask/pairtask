package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/pairtask/pairtask/internal/peer"
)

// ErrNoContact is a name for an identity that is no contact here.
var ErrNoContact = errors.New("no such contact")

// ContactName is what this side's operator calls a contact, "" for none,
// under the operator audit row the change finishes.
type ContactName struct {
	Contact, Name string
	AuditID       int64
}

// SetContactLocalName sets what this side calls a contact and finishes its
// audit row, in one transaction; the identity, its pins and grants and the
// history stay as they are. It returns the name it had.
func (s *Store) SetContactLocalName(ctx context.Context, r ContactName) (string, error) {
	if r.Name != "" {
		if err := peer.CheckPairingName(r.Name); err != nil {
			return "", err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	c, found, err := getContact(ctx, tx, r.Contact)
	if err != nil || !found {
		return "", errors.Join(err, notFound(found))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE peer_contacts SET local_name = ? WHERE identity_key = ?`, r.Name, r.Contact); err != nil {
		return "", err
	}
	detail := fmt.Sprintf("%q -> %q", c.LocalName, r.Name)
	if err := finishAudit(ctx, tx, r.AuditID, AuditOutcome{Result: AuditAccepted, Detail: detail}); err != nil {
		return "", err
	}
	return c.LocalName, tx.Commit()
}

func notFound(found bool) error {
	if found {
		return nil
	}
	return ErrNoContact
}
