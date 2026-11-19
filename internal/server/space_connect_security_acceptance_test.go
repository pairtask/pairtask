//go:build linux

package server

import (
	"crypto"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/pairbook"
)

func TestSpaceConnectionAcceptanceResponderChecksOwnVerifiedRoster(t *testing.T) {
	for _, missing := range []string{"self", "dialer"} {
		t.Run(missing, func(t *testing.T) {
			p := newConnectPair(t)
			members := p.directory.roster.Members[:1]
			if missing == "dialer" {
				members = p.directory.roster.Members[1:]
			}
			p.directory.mu.Lock()
			p.directory.rosters[p.b.identity.Fingerprint()] = pairbook.Roster{Members: members}
			p.directory.mu.Unlock()
			out := p.a.connect(p.b.identity.Fingerprint())
			operatorEqual(t, "responder must verify both memberships", out.Type, protocol.TypeError)
			assertNoConnectContact(t, p.a)
			assertNoConnectContact(t, p.b)
		})
	}
}

func TestSpaceConnectionAcceptanceSelfConnectCreatesNothing(t *testing.T) {
	p := newConnectPair(t)
	out := p.a.connect(p.a.identity.Fingerprint())
	operatorEqual(t, "self connect refused", out.Type, protocol.TypeError)
	operatorEqual(t, "self connect never dials", p.a.dials.Load(), int32(0))
	assertNoConnectContact(t, p.a)
}

func TestSpaceConnectionAcceptanceForgedAddressNeverDials(t *testing.T) {
	p := newConnectPair(t)
	p.directory.mu.Lock()
	key := p.b.identity.Fingerprint()
	record := p.directory.addresses[key]
	record.Sig = append([]byte(nil), record.Sig...)
	record.Sig[0] ^= 1
	p.directory.addresses[key] = record
	p.directory.mu.Unlock()
	out := p.a.connect(key)
	operatorEqual(t, "forged address refused", out.Type, protocol.TypeError)
	operatorEqual(t, "forged address never dialed", p.a.dials.Load(), int32(0))
	assertNoConnectContact(t, p.a)
	assertNoConnectContact(t, p.b)
}

func TestSpaceConnectionAcceptanceTriesNextVerifiedAddress(t *testing.T) {
	p := newConnectPair(t)
	p.directory.mu.Lock()
	key := p.b.identity.Fingerprint()
	record := p.directory.addresses[key].Address
	record.Addrs = []string{"127.0.0.1:1", p.b.address}
	sig, err := p.b.identity.Sign(nil, record.SigningBytes(), crypto.Hash(0))
	operatorNoError(t, err)
	p.directory.addresses[key] = pairbook.SignedAddress{Address: record, Sig: sig}
	p.directory.mu.Unlock()
	out := requireConnected(t, p.a.connect(key))
	operatorEqual(t, "successful address saved", out.Destination, p.b.address)
}

func TestSpaceConnectionAcceptanceCarriesOperatorMessagesWithoutAgentGrants(t *testing.T) {
	p := newConnectPair(t)
	requireConnected(t, p.a.connect(p.b.identity.Fingerprint()))
	out := p.a.h.SubmitOperator(p.a.ctx, 0, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend, ContactKey: p.b.identity.Fingerprint(), MessageID: "space-first-message", ContactText: "hello through verified space"})
	operatorEqual(t, "message queued", out.Type, protocol.TypeOperatorContactResult)
	awaitMessageDelivery(t, p.a.st.PeerMessages(), p.b.identity.Fingerprint(), "space-first-message")
	var text string
	operatorNoError(t, p.b.db.QueryRow(`SELECT text FROM peer_messages WHERE contact=? AND direction='in' AND message_id='space-first-message'`, p.a.identity.Fingerprint()).Scan(&text))
	operatorEqual(t, "delivered text", text, "hello through verified space")
	assertConnectNoGrants(t, p.a)
	assertConnectNoGrants(t, p.b)
}
