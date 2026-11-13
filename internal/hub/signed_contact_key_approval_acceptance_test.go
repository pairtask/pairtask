package hub

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

func TestSignedContactKeyApprovalAcceptanceOnlySeenKeyCanBeApproved(t *testing.T) {
	h, contact := remoteHubFixture(t)
	approve, parsed, personID := seedPendingContactPerson(t, h, contact)
	ctx := context.Background()
	list := h.operatorContact(ctx, nil, operatorRequest{uid: 0, msg: protocol.Message{
		Type: protocol.TypeOperatorContact, ProtocolVersion: protocol.SealVersion,
		ContactOp: protocol.ContactOpList, ContactKey: contact, MessageID: "list-before-approval",
	}})
	page := requireSignedContactPage(t, list)
	if len(page.PendingPersonKeys) != 1 {
		t.Fatalf("operator page did not name one pending contact key: %+v", page)
	}
	if page.PendingPersonKeys[0] != personID {
		t.Fatalf("operator page named pending key %s, want %s", page.PendingPersonKeys[0], personID)
	}
	out := h.operatorContact(ctx, nil, operatorRequest{uid: 0, msg: approve})
	page = requireSignedContactPage(t, out)
	if len(page.PendingPersonKeys) != 0 {
		t.Fatalf("approved key still shown as pending: %+v", page)
	}
	roots, err := h.signingRoots(ctx)
	messageWorkerOK(t, err)
	got := signing.Verify(&parsed, protocol.RootsOf(roots), time.Now())
	if got.State != signing.Verified || got.Signer == nil {
		t.Fatalf("approved contact key did not verify: %+v", got)
	}
	if got.Signer.Basis != signing.BasisPerson {
		t.Fatalf("approved contact key did not verify: %+v", got)
	}
}

func requireSignedContactPage(t *testing.T, out protocol.Message) *protocol.ContactMessagePage {
	t.Helper()
	if out.Type != protocol.TypeOperatorContactResult {
		t.Fatalf("contact page request failed: %+v", out)
	}
	if out.ContactPage == nil {
		t.Fatalf("contact page was omitted: %+v", out)
	}
	return out.ContactPage
}

func seedPendingContactPerson(t *testing.T, h *Hub, contact string) (protocol.Message, signing.Seal, string) {
	t.Helper()
	ctx := context.Background()
	if problem := h.loadIdentity(ctx); problem != "" {
		t.Fatal(problem)
	}
	_, person, err := ed25519.GenerateKey(nil)
	messageWorkerOK(t, err)
	_, device, err := ed25519.GenerateKey(nil)
	messageWorkerOK(t, err)
	personPublic := person.Public().(ed25519.PublicKey)
	personID := signing.KeyID(personPublic)
	approve := protocol.Message{Type: protocol.TypeOperatorContact, ProtocolVersion: protocol.SealVersion,
		ContactOp: protocol.ContactOpAcceptKey, ContactKey: contact, ContactText: personID, MessageID: "approve-contact-person"}
	if out := h.operatorContact(ctx, nil, operatorRequest{uid: 0, msg: approve}); out.Type != protocol.TypeError || out.Code != protocol.ErrContactRefused {
		t.Fatalf("unseen person key was approvable: %+v", out)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	cert := signing.Issue(person, signing.Cert{Kind: signing.KindDevice, Subject: "contact-chat", Key: device.Public().(ed25519.PublicKey),
		Workspace: contact, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)})
	m := messaging.Message{ID: "signed-before-approval", N: 1, CreatedAt: now, Text: "hello from contact"}
	header := signing.Header{MessageID: m.ID, SenderKind: signing.SenderOperator, SenderID: protocol.OperatorID,
		Workspace: contact, Recipients: (protocol.PeerLink{Receiver: h.identity.Fingerprint()}).Recipients(),
		CreatedAt: now, TextHash: signing.TextHash(m.Text)}
	m.Seal = signing.Sign(device, []signing.Cert{cert}, personPublic, header).Marshal()
	a, err := h.store.PeerMessages().Accept(ctx, contact, m, now)
	messageWorkerOK(t, err)
	if !a.Stored {
		t.Fatalf("pending person's valid message was not retained: %+v", a)
	}
	roots, err := h.signingRoots(ctx)
	messageWorkerOK(t, err)
	parsed, err := signing.ParseSeal(m.Seal)
	messageWorkerOK(t, err)
	if got := signing.Verify(&parsed, protocol.RootsOf(roots), time.Now()); got.State != signing.Unverified {
		t.Fatalf("unapproved contact key already trusted: %+v", got)
	}
	return approve, parsed, personID
}

func TestSignedContactKeyApprovalAcceptanceRequireSealsOperatorSwitch(t *testing.T) {
	h, contact := remoteHubFixture(t)
	ctx := context.Background()
	if required, err := h.store.PeerMessages().RequiresSeals(ctx, contact); err != nil || required {
		t.Fatalf("fresh contact policy: required=%t err=%v", required, err)
	}
	assertRequireSealsChoice(t, h, contact, requireSealsChoice{"require-on", "on", true})
	m := messaging.Message{ID: "unsigned-under-require", N: 1, CreatedAt: time.Now(), Text: "unsigned"}
	a, err := h.store.PeerMessages().AcceptVersion(ctx, contact, messaging.Arrival{Version: int(messaging.SealVersion), Message: m, At: time.Now()})
	messageWorkerOK(t, err)
	if a.Stored {
		t.Fatalf("required contact accepted unsigned message: %+v", a)
	}
	if a.Reason != messaging.ReasonUnsigned {
		t.Fatalf("required contact refused for %q, want unsigned", a.Reason)
	}
	assertRequireSealsChoice(t, h, contact, requireSealsChoice{"require-off", "off", false})
	a, err = h.store.PeerMessages().AcceptVersion(ctx, contact, messaging.Arrival{Version: int(messaging.SealVersion), Message: m, At: time.Now()})
	if err != nil || !a.Stored {
		t.Fatalf("unsigned migration did not resume after off: answer=%+v err=%v", a, err)
	}
}

func TestSignedContactKeyApprovalAcceptanceInvalidRequireChoice(t *testing.T) {
	h, contact := remoteHubFixture(t)
	out := requireSealsCommand(h, contact, "bad-choice", "maybe")
	if out.Type != protocol.TypeError || out.Code != protocol.ErrMalformedFrame {
		t.Fatalf("invalid policy choice accepted: %+v", out)
	}
}

type requireSealsChoice struct {
	id, choice string
	want       bool
}

func assertRequireSealsChoice(t *testing.T, h *Hub, contact string, choice requireSealsChoice) {
	t.Helper()
	page := requireSignedContactPage(t, requireSealsCommand(h, contact, choice.id, choice.choice))
	if page.RequireSeals != choice.want {
		t.Fatalf("operator require-seals=%t, want %t", page.RequireSeals, choice.want)
	}
}

func requireSealsCommand(h *Hub, contact, id, choice string) protocol.Message {
	return h.operatorContact(context.Background(), nil, operatorRequest{uid: 0, msg: protocol.Message{
		Type: protocol.TypeOperatorContact, ProtocolVersion: protocol.SealVersion,
		ContactOp: protocol.ContactOpRequireSeals, ContactKey: contact, ContactText: choice, MessageID: id,
	}})
}
