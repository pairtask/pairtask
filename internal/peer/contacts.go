package peer

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
)

// Contacts (#178) are workspaces this one has paired with in either
// direction, each confirmed by a durable decision: as joiner, the inviter's
// authenticated acceptance; as inviter, the joiner's acknowledgement of it.
// There is one contact per identity. A rejection never makes one.

// ErrContactCapacity refuses a new contact to a full store.
var ErrContactCapacity = errors.New("peer: contact capacity reached")

// Contact is one paired workspace.
type Contact struct {
	IdentityKey string
	Name        string
	Destination string // where to reach it, when this side redeemed its code
	// DestinationAt orders destinations: when the redemption that gave
	// Destination started. A newer completed redemption moves the contact.
	DestinationAt time.Time
	EstablishedAt time.Time
	Joined        bool // this workspace redeemed its code
	Invited       bool // it redeemed this workspace's code
}

// ContactStore reads contacts.
type ContactStore interface {
	Contacts(ctx context.Context) ([]Contact, error)
	Contact(ctx context.Context, identityKey string) (Contact, bool, error)
}

// MergeContact adds one direction to an identity's contact: the flags
// accumulate, a known name is kept unless empty, the destination of the
// newest redemption wins, and the first establishment time stays.
func MergeContact(existing Contact, found bool, c Contact) Contact {
	if !found {
		return c
	}
	existing.Joined = existing.Joined || c.Joined
	existing.Invited = existing.Invited || c.Invited
	existing.Name = firstNonEmpty(existing.Name, c.Name)
	if NewerDestination(existing, c) {
		existing.Destination, existing.DestinationAt = c.Destination, c.DestinationAt
	}
	return existing
}

// NewerDestination: c's destination replaces existing's. One fills an
// empty destination; otherwise only a redemption that started later moves
// it, the same address too, so an older one can never roll it back.
func NewerDestination(existing, c Contact) bool {
	if c.Destination == "" {
		return false
	}
	return existing.Destination == "" || c.DestinationAt.After(existing.DestinationAt)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// AdmitContact refuses a new identity to a full store; a known one always
// updates.
func AdmitContact(found bool, total int) error {
	if !found && total >= MaxPairingIdentities {
		return ErrContactCapacity
	}
	return nil
}

// MemContacts is an in-memory contact set that memory stores can share.
type MemContacts struct {
	mu       sync.Mutex
	contacts map[string]Contact
}

func NewMemContacts() *MemContacts {
	return &MemContacts{contacts: map[string]Contact{}}
}

// apply admits and merges one direction and, under the same lock, runs
// commit, the caller's own state change, storing the contact only if it
// succeeds. Callers hold their store's lock first, so the order is always
// store then contacts.
func (m *MemContacts) apply(c Contact, commit func()) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, found := m.contacts[c.IdentityKey]
	if err := AdmitContact(found, len(m.contacts)); err != nil {
		return err
	}
	commit()
	m.contacts[c.IdentityKey] = MergeContact(existing, found, c)
	return nil
}

func (m *MemContacts) Contacts(context.Context) ([]Contact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Contact, 0, len(m.contacts))
	for _, c := range m.contacts {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Contact) int { return strings.Compare(a.IdentityKey, b.IdentityKey) })
	return out, nil
}

func (m *MemContacts) Contact(_ context.Context, identityKey string) (Contact, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.contacts[identityKey]
	return c, ok, nil
}
