package peer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Contacts (#178) are workspaces this one has paired with in either
// direction, each confirmed by a durable decision: as joiner, the inviter's
// authenticated acceptance; as inviter, the joiner's acknowledgement of it.
// There is one contact per identity. A rejection never makes one.

// ErrContactCapacity refuses one more active contact to a store holding
// MaxActiveContacts; it is an ErrIdentityStorage.
var ErrContactCapacity = fmt.Errorf("peer: contact capacity reached: %w", ErrIdentityStorage)

var (
	// ErrDestinationChanged refuses an address change whose contact moved
	// meanwhile: the newer destination stays.
	ErrDestinationChanged = errors.New("their address changed meanwhile; nothing changed")
	// ErrNotDialing refuses an address change on the side that doesn't
	// dial the contact.
	ErrNotDialing = errors.New("this side doesn't dial them; they must update your address")
)

// Contact is one paired workspace.
type Contact struct {
	IdentityKey string
	Name        string
	Destination string // where to reach it, when this side redeemed its code
	// LocalName is what this side's operator calls it; never from the peer.
	LocalName string
	// DestinationAt orders destinations: when the redemption that gave
	// Destination started. A newer completed redemption moves the contact.
	DestinationAt time.Time
	EstablishedAt time.Time
	Joined        bool // this workspace redeemed its code
	Invited       bool // it redeemed this workspace's code
	// PausedAt is when this side's operator paused it (#276), zero while
	// active; Generation moves on with each pause and resume.
	PausedAt   time.Time
	Generation int64
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
	existing.Name = mergedName(existing.Name, c.Name)
	if NewerDestination(existing, c) {
		existing.Destination, existing.DestinationAt = c.Destination, c.DestinationAt
	}
	return existing
}

// DefaultPairingName is the name a workspace pairs under unless its
// operator names it.
const DefaultPairingName = "workspace"

// mergedName keeps a known name; an offered one replaces none, or the
// default, which names nobody.
func mergedName(existing, offered string) string {
	if offered == "" || !unnamed(existing) {
		return existing
	}
	return offered
}

// unnamed: no name, or the default, which names nobody.
func unnamed(name string) bool {
	return name == "" || name == DefaultPairingName
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

// ContactSlots is what the contact bounds count, read with the write they
// admit: the contacts not unpaired, and every identity row retained.
type ContactSlots struct {
	Active, Retained int
}

// AdmitContact refuses a new identity to a full store: ErrIdentityStorage
// beyond MaxRetainedIdentities, ErrContactCapacity beyond
// MaxActiveContacts. A known one always updates.
func AdmitContact(found bool, slots ContactSlots) error {
	switch {
	case found:
		return nil
	case slots.Retained >= MaxRetainedIdentities:
		return ErrIdentityStorage
	}
	return AdmitActive(slots)
}

// AdmitActive refuses to make one more contact active beyond
// MaxActiveContacts, as pairing an unpaired one again does.
func AdmitActive(slots ContactSlots) error {
	if slots.Active >= MaxActiveContacts {
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
	if err := AdmitContact(found, ContactSlots{Active: len(m.contacts), Retained: len(m.contacts)}); err != nil {
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
