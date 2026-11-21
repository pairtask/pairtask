package messaging_test

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

func TestIdentityRevocationAfterFullPagePrecedesMessage(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("../../identity/testdata/wire-v1.json")
	sessionOK(t, err)
	var packet struct {
		Fixtures map[string]struct {
			Seed string `json:"public_test_seed_hex"`
		}
		Vectors []struct {
			Name string
			Wire string `json:"wire_hex"`
		}
	}
	sessionOK(t, json.Unmarshal(raw, &packet))
	keys := func(label string) ed25519.PrivateKey {
		b, err := hex.DecodeString(packet.Fixtures[label].Seed)
		sessionOK(t, err)
		return ed25519.NewKeyFromSeed(b)
	}
	wires := map[string][]byte{}
	for _, v := range packet.Vectors {
		wires[v.Name], err = hex.DecodeString(v.Wire)
		sessionOK(t, err)
	}
	w := keys("W0").Public().(ed25519.PublicKey)
	sender := identity.KeyID(w)
	receiver := sessionContact
	left, _ := sessionFixtureFor(t, receiver)
	right, _ := sessionFixtureFor(t, sender)
	sessionOK(t, left.InitIdentity(ctx, sender))
	sessionOK(t, right.InitIdentity(ctx, receiver))
	l, err := left.PeerMessages().Admit(ctx, receiver)
	sessionOK(t, err)
	r, err := right.PeerMessages().Admit(ctx, sender)
	sessionOK(t, err)
	anchor, err := identity.ParseAnchor(wires["anchor"])
	sessionOK(t, err)
	_, err = r.ApplyIdentity(ctx, wires["anchor"], sessionAt)
	sessionOK(t, err)
	sessionOK(t, right.ApproveIdentity(ctx, anchor.Lineage(), sessionAt))
	sessionOK(t, right.NoteWorkspaceKey(ctx, w, sessionAt))
	record, err := identity.ParseRecord(wires["record"])
	sessionOK(t, err)
	person := keys("P0")
	for i := 0; i < messaging.SessionMessages; i++ {
		record.Version = uint64(i + 1)
		sessionOK(t, left.QueueIdentity(ctx, receiver, record.Signed(person).Marshal(), sessionAt.Add(time.Duration(i)*time.Second)))
	}
	device := keys("D0")
	cert, err := signing.ParseCert(wires["device"])
	sessionOK(t, err)
	cert.NotBefore = time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	cert.NotAfter = cert.NotBefore.Add(time.Hour)
	cert = signing.Issue(person, cert)
	revoke := identity.Revocation{Lineage: anchor.Lineage(), Epoch: 1, Device: identity.KeyID(device.Public().(ed25519.PublicKey)), At: cert.NotBefore}
	sessionOK(t, left.QueueIdentity(ctx, receiver, revoke.Signed(person).Marshal(), sessionAt.Add(time.Hour)))
	header := signing.Header{MessageID: "late-revocation", SenderKind: signing.SenderOperator, SenderID: protocol.OperatorID, Workspace: sender, Recipients: (protocol.PeerLink{Contact: sender, Receiver: receiver}).Recipients(), CreatedAt: cert.NotBefore, TextHash: signing.TextHash("hello")}
	sealed := signing.Sign(device, []signing.Cert{cert}, anchor.Person, header)
	_, err = left.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: receiver, ID: header.MessageID, Text: "hello", Now: sessionAt, Seal: sealed.Marshal()})
	sessionOK(t, err)
	round := func() []sessionOutcome {
		out := exchangeContacts(t, exchangeEndpoint{identityEnabled{l}, receiver}, exchangeEndpoint{identityEnabled{r}, sender}, 0)
		sessionSuccess(t, out)
		return out
	}
	first := round()
	for _, o := range first {
		sessionEqual(t, o.result.IdentityMore, true)
		sessionEqual(t, o.result.Sent+o.result.Received+o.result.Refused, 0)
	}
	held, found, err := right.HeldIdentity(ctx, anchor.Lineage())
	sessionOK(t, err)
	sessionEqual(t, found, true)
	sessionEqual(t, held.Version, uint64(messaging.SessionMessages))
	sessionEqual(t, len(held.Revoked), 0)
	frame := signing.Frame{MessageID: header.MessageID, SenderKind: header.SenderKind, SenderID: header.SenderID, Text: "hello", Addresses: []string{(protocol.PeerLink{Contact: sender, Receiver: receiver}).Address()}}
	before, _ := identity.Admit(sealed, frame, held, identity.Clock{Now: time.Now})
	if before.State != signing.Verified {
		t.Fatalf("message already invalid before revocation: %+v", before)
	}
	second := round()
	if second[0].result.Refused+second[1].result.Refused != 1 {
		t.Fatalf("expected revoked message refused: %+v", second)
	}
	held, _, err = right.HeldIdentity(ctx, anchor.Lineage())
	sessionOK(t, err)
	sessionEqual(t, len(held.Revoked), 1)
	sessionEqual(t, held.Revoked[0], revoke.Device)
	after, _ := identity.Admit(sealed, frame, held, identity.Clock{Now: time.Now})
	sessionEqual(t, after.State, signing.Invalid)
	sessionEqual(t, after.Reason, "revoked device")
	for _, o := range round() {
		sessionEqual(t, o.result.IdentityReceived+o.result.Received+o.result.Refused, 0)
	}
}
