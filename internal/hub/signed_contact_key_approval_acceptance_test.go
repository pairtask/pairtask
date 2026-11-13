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
	list := h.operatorContact(ctx, nil, operatorRequest{uid: 0, msg: protocol.Message{
		Type: protocol.TypeOperatorContact, ProtocolVersion: protocol.SealVersion,
		ContactOp: protocol.ContactOpList, ContactKey: contact, MessageID: "list-before-approval",
	}})
	if list.Type != protocol.TypeOperatorContactResult || list.ContactPage == nil || len(list.ContactPage.PendingPersonKeys) != 1 || list.ContactPage.PendingPersonKeys[0] != personID {
		t.Fatalf("operator page did not name pending contact key: %+v", list)
	}
	out := h.operatorContact(ctx, nil, operatorRequest{uid: 0, msg: approve})
	if out.Type != protocol.TypeOperatorContactResult {
		t.Fatalf("operator could not accept seen contact key: %+v", out)
	}
	if out.ContactPage == nil || len(out.ContactPage.PendingPersonKeys) != 0 {
		t.Fatalf("approved key still shown as pending: %+v", out.ContactPage)
	}
	roots, err = h.signingRoots(ctx)
	messageWorkerOK(t, err)
	if got := signing.Verify(&parsed, protocol.RootsOf(roots), time.Now()); got.State != signing.Verified || got.Signer == nil || got.Signer.Basis != signing.BasisPerson {
		t.Fatalf("approved contact key did not verify: %+v", got)
	}
}

func TestSignedContactKeyApprovalAcceptanceRequireSealsOperatorSwitch(t *testing.T) {
	h, contact := remoteHubFixture(t)
	ctx := context.Background()
	set := func(id, choice string) protocol.Message {
		return h.operatorContact(ctx, nil, operatorRequest{uid: 0, msg: protocol.Message{
			Type: protocol.TypeOperatorContact, ProtocolVersion: protocol.SealVersion,
			ContactOp: protocol.ContactOpRequireSeals, ContactKey: contact, ContactText: choice, MessageID: id,
		}})
	}
	if required, err := h.store.PeerMessages().RequiresSeals(ctx, contact); err != nil || required {
		t.Fatalf("fresh contact policy: required=%t err=%v", required, err)
	}
	if out := set("bad-choice", "maybe"); out.Type != protocol.TypeError || out.Code != protocol.ErrMalformedFrame {
		t.Fatalf("invalid policy choice accepted: %+v", out)
	}
	if out := set("require-on", "on"); out.Type != protocol.TypeOperatorContactResult || out.ContactPage == nil || !out.ContactPage.RequireSeals {
		t.Fatalf("operator could not require seals: %+v", out)
	}
	m := messaging.Message{ID: "unsigned-under-require", N: 1, CreatedAt: time.Now(), Text: "unsigned"}
	a, err := h.store.PeerMessages().AcceptVersion(ctx, contact, messaging.Arrival{Version: int(messaging.SealVersion), Message: m, At: time.Now()})
	if err != nil || a.Stored || a.Reason != messaging.ReasonUnsigned {
		t.Fatalf("required contact's unsigned message was accepted: answer=%+v err=%v", a, err)
	}
	if out := set("require-off", "off"); out.Type != protocol.TypeOperatorContactResult || out.ContactPage == nil || out.ContactPage.RequireSeals {
		t.Fatalf("operator could not relax policy: %+v", out)
	}
	a, err = h.store.PeerMessages().AcceptVersion(ctx, contact, messaging.Arrival{Version: int(messaging.SealVersion), Message: m, At: time.Now()})
	if err != nil || !a.Stored {
		t.Fatalf("unsigned migration did not resume after off: answer=%+v err=%v", a, err)
	}
}
