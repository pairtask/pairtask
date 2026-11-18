//go:build linux

package server

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/pairbook"
)

func TestSpaceConnectionAcceptanceTwoHubsInstallSameConfirmedPairing(t *testing.T) {
	p := newConnectPair(t)
	assertNoConnectContact(t, p.a)
	assertNoConnectContact(t, p.b)
	got := requireConnected(t, p.a.connect(p.b.identity.Fingerprint()))
	operatorEqual(t, "target", got.IdentityKey, p.b.identity.Fingerprint())
	operatorEqual(t, "signed member name", got.Name, "Bob")
	operatorEqual(t, "verified destination", got.Destination, p.b.address)
	operatorEqual(t, "confirmed", got.Confirmed, true)
	operatorEqual(t, "epoch", got.PairingEpoch, int64(1))
	aEpoch, aID := p.a.pairing(t, p.b.identity.Fingerprint())
	bEpoch, bID := p.b.pairing(t, p.a.identity.Fingerprint())
	operatorEqual(t, "both epochs", aEpoch, bEpoch)
	operatorEqual(t, "pairing ID size", len(aID), 32)
	operatorEqual(t, "same pairing ID", bytes.Equal(aID, bID), true)
	inbound, found, err := p.b.st.Contact(p.b.ctx, p.a.identity.Fingerprint())
	operatorNoError(t, err)
	operatorEqual(t, "inbound contact", found, true)
	operatorEqual(t, "inbound name", inbound.Name, "Alice")
	operatorEqual(t, "inbound direction", inbound.Invited, true)
	operatorEqual(t, "inbound has no destination", inbound.Destination, "")
	assertConnectNoGrants(t, p.a)
	assertConnectNoGrants(t, p.b)
}

func TestSpaceConnectionAcceptanceExistingContactKeepsEpoch(t *testing.T) {
	p := newConnectPair(t)
	first := requireConnected(t, p.a.connect(p.b.identity.Fingerprint()))
	before, id := p.a.pairing(t, p.b.identity.Fingerprint())
	// An existing confirmed contact remains usable even if its directory
	// address is gone; connecting again must not rotate its pairing.
	p.directory.mu.Lock()
	clear(p.directory.addresses)
	p.directory.mu.Unlock()
	again := requireConnected(t, p.a.connect(p.b.identity.Fingerprint()))
	after, nextID := p.a.pairing(t, p.b.identity.Fingerprint())
	operatorEqual(t, "same contact", again.IdentityKey, first.IdentityKey)
	operatorEqual(t, "same epoch", after, before)
	operatorEqual(t, "same pairing", bytes.Equal(id, nextID), true)
}

func TestSpaceConnectionAcceptanceUnverifiedMembershipNeverDials(t *testing.T) {
	for _, missing := range []string{"self", "target", "revoked-target"} {
		t.Run(missing, func(t *testing.T) {
			p := newConnectPair(t)
			p.directory.mu.Lock()
			switch missing {
			case "self":
				p.directory.roster.Members = p.directory.roster.Members[1:]
			case "target":
				p.directory.roster.Members = p.directory.roster.Members[:1]
			case "revoked-target":
				p.directory.roster.Revocations = []pairbook.SignedRevocation{pairbook.SignRevocation(p.owner, pairbook.Revocation{Space: connectSpaceID, Node: p.b.identity.PublicKey(), By: p.owner.Public().(ed25519.PublicKey), At: time.Now().UnixMilli()})}
			}
			p.directory.mu.Unlock()
			out := p.a.connect(p.b.identity.Fingerprint())
			operatorEqual(t, "unverified membership refused", out.Type, protocol.TypeError)
			operatorEqual(t, "no dial", p.a.dials.Load(), int32(0))
			assertNoConnectContact(t, p.a)
			assertNoConnectContact(t, p.b)
		})
	}
}

func TestSpaceConnectionAcceptanceWrongTLSIdentityCreatesNothing(t *testing.T) {
	p := newConnectPair(t)
	// A genuinely signed address record can still point at a replaced host.
	// The TLS fingerprint, not the directory's host address, is authoritative.
	p.directory.mu.Lock()
	key := p.b.identity.Fingerprint()
	record := p.directory.addresses[key].Address
	record.Addrs = []string{p.a.address}
	sig, err := p.b.identity.Sign(nil, record.SigningBytes(), crypto.Hash(0))
	operatorNoError(t, err)
	p.directory.addresses[key] = pairbook.SignedAddress{Address: record, Sig: sig}
	p.directory.mu.Unlock()
	out := p.a.connect(key)
	operatorEqual(t, "wrong TLS key refused", out.Type, protocol.TypeError)
	assertNoConnectContact(t, p.a)
	assertNoConnectContact(t, p.b)
}

func TestSpaceConnectionAcceptanceResponderRequiresLocalPin(t *testing.T) {
	p := newConnectPair(t)
	operatorNoError(t, p.b.st.UnpinSpace(p.b.ctx, connectSpaceID))
	out := p.a.connect(p.b.identity.Fingerprint())
	operatorEqual(t, "responder without pin refused", out.Type, protocol.TypeError)
	assertNoConnectContact(t, p.a)
	assertNoConnectContact(t, p.b)
}

func TestSpaceConnectionAcceptanceLostAnswerRetriesSameAttempt(t *testing.T) {
	p := newConnectPair(t)
	p.b.dropNext.Store(true)
	out := p.a.connect(p.b.identity.Fingerprint())
	operatorEqual(t, "lost answer reports failure", out.Type, protocol.TypeError)
	select {
	case <-p.b.dropped:
	case <-time.After(3 * time.Second):
		t.Fatal("responder did not commit before connection loss")
	}
	assertNoConnectContact(t, p.a)
	before, id := p.b.pairing(t, p.a.identity.Fingerprint())
	requireConnected(t, p.a.connect(p.b.identity.Fingerprint()))
	after, nextID := p.b.pairing(t, p.a.identity.Fingerprint())
	aEpoch, aID := p.a.pairing(t, p.b.identity.Fingerprint())
	operatorEqual(t, "responder did not reinstall", after, before)
	operatorEqual(t, "responder kept pairing ID", bytes.Equal(nextID, id), true)
	operatorEqual(t, "dialer completed same pairing", bytes.Equal(aID, id), true)
	operatorEqual(t, "same installed epoch", aEpoch, after)
	assertConnectNoGrants(t, p.a)
	assertConnectNoGrants(t, p.b)
}
