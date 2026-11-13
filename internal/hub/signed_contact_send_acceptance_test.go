package hub

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
	"github.com/pairtask/pairtask/internal/store"
)

func signedContactSendFixture(t *testing.T) (*Hub, string, ed25519.PrivateKey, ed25519.PrivateKey) {
	t.Helper()
	h, contact := remoteHubFixture(t)
	if problem := h.loadIdentity(context.Background()); problem != "" {
		t.Fatal(problem)
	}
	_, person, err := ed25519.GenerateKey(nil)
	messageWorkerOK(t, err)
	_, device, err := ed25519.GenerateKey(nil)
	messageWorkerOK(t, err)
	register := protocol.Message{Type: protocol.TypeOperatorIdentity, IdentityOp: protocol.IdentityOpRegister,
		IdentityKey: base64.StdEncoding.EncodeToString(person.Public().(ed25519.PublicKey)), MessageID: "register-person"}
	if out := h.operatorIdentity(context.Background(), nil, operatorRequest{uid: 0, msg: register}); out.Type != protocol.TypeOperatorIdentityResult {
		t.Fatalf("operator's person key was not registered: %+v", out)
	}
	return h, contact, person, device
}

type contactSendSigner struct {
	h       *Hub
	contact string
	person  ed25519.PrivateKey
	device  ed25519.PrivateKey
}

func (s contactSendSigner) message(id, body string) protocol.Message {
	now := time.Now().UTC().Truncate(time.Millisecond)
	cert := signing.Issue(s.person, signing.Cert{
		Kind: signing.KindDevice, Subject: "chat", Key: s.device.Public().(ed25519.PublicKey),
		Workspace: s.h.identity.Fingerprint(), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
	})
	header := signing.Header{MessageID: id, SenderKind: signing.SenderOperator, SenderID: protocol.OperatorID,
		Workspace: s.h.identity.Fingerprint(), Recipients: (protocol.PeerLink{Receiver: s.contact}).Recipients(),
		CreatedAt: now, TextHash: signing.TextHash(body)}
	seal := signing.Sign(s.device, []signing.Cert{cert}, s.person.Public().(ed25519.PublicKey), header)
	return protocol.Message{Type: protocol.TypeOperatorContact, ProtocolVersion: protocol.SealVersion,
		ContactOp: protocol.ContactOpSend, ContactKey: s.contact, ContactText: body, MessageID: id,
		Seal: protocol.EncodeSeal(seal)}
}

func TestSignedContactSendAcceptanceSealPersistsAcrossRetry(t *testing.T) {
	h, contact, person, device := signedContactSendFixture(t)
	msg := (contactSendSigner{h, contact, person, device}).message("contact-send-signed", "hello contact")
	request := operatorRequest{uid: 0, msg: msg}
	first := h.operatorContact(context.Background(), nil, request)
	assertContactSendResult(t, first, msg.Seal)
	assertQueuedContactSeal(t, h, contact, msg.Seal)
	retry := h.operatorContact(context.Background(), nil, request)
	assertContactSendResult(t, retry, msg.Seal)
	assertQueuedContactSeal(t, h, contact, msg.Seal)
}

func assertContactSendResult(t *testing.T, out protocol.Message, seal string) {
	t.Helper()
	if out.Type != protocol.TypeOperatorContactResult || out.ContactMessage == nil {
		t.Fatalf("signed contact send failed: %+v", out)
	}
	if out.ContactMessage.Seal != seal {
		t.Fatalf("contact send result lost operator seal: %+v", out.ContactMessage)
	}
}

func assertQueuedContactSeal(t *testing.T, h *Hub, contact, seal string) {
	t.Helper()
	rows, _, err := h.store.PeerMessages().Page(context.Background(), contact, 0, 10)
	messageWorkerOK(t, err)
	parsed, err := protocol.DecodeSeal(seal)
	messageWorkerOK(t, err)
	if len(rows) != 1 {
		t.Fatalf("contact send queued %d rows, want 1", len(rows))
	}
	if !bytes.Equal(rows[0].Seal, parsed.Marshal()) {
		t.Fatalf("contact send queued a different seal: %+v", rows[0])
	}
}

func TestSignedContactSendAcceptanceTamperingAndUnapprovedPersonAreRefused(t *testing.T) {
	h, contact, person, device := signedContactSendFixture(t)
	signer := contactSendSigner{h, contact, person, device}
	for name, change := range map[string]func(*protocol.Message){
		"text":      func(m *protocol.Message) { m.ContactText = "altered text" },
		"recipient": func(m *protocol.Message) { m.ContactTo = []string{"agent-elsewhere"} },
		"signature": func(m *protocol.Message) { m.Seal = base64.StdEncoding.EncodeToString([]byte("forged")) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := signer.message("tamper-"+name, "untampered")
			change(&bad)
			out := h.operatorContact(context.Background(), nil, operatorRequest{uid: 0, msg: bad})
			if out.Type != protocol.TypeError || out.Code != protocol.ErrInvalidSeal {
				t.Fatalf("altered contact send was accepted: %+v", out)
			}
		})
	}
	_, unapprovedPerson, err := ed25519.GenerateKey(nil)
	messageWorkerOK(t, err)
	_, unapprovedDevice, err := ed25519.GenerateKey(nil)
	messageWorkerOK(t, err)
	unapproved := (contactSendSigner{h, contact, unapprovedPerson, unapprovedDevice}).message("unapproved-person", "who is this")
	if out := h.operatorContact(context.Background(), nil, operatorRequest{uid: 0, msg: unapproved}); out.Type != protocol.TypeError || out.Code != protocol.ErrInvalidSeal {
		t.Fatalf("unregistered person key sent as operator: %+v", out)
	}
	rows, _, err := h.store.PeerMessages().Page(context.Background(), contact, 0, 10)
	messageWorkerOK(t, err)
	if len(rows) != 0 {
		t.Fatalf("altered contact send persisted: %+v", rows)
	}
}

func TestSignedContactSendAcceptanceContactPersonCannotSignAsOurOperator(t *testing.T) {
	h, contact, _, _ := signedContactSendFixture(t)
	ctx := context.Background()
	const id, body = "foreign-person-send", "this workspace did not sign"
	header := signing.Header{MessageID: id, SenderKind: signing.SenderOperator, SenderID: protocol.OperatorID,
		Recipients: (protocol.PeerLink{Receiver: contact}).Recipients(), TextHash: signing.TextHash(body)}
	seal := foreignContactPersonSeal(t, h, contact, header)
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ProtocolVersion: protocol.SealVersion,
		ContactOp: protocol.ContactOpSend, ContactKey: contact, ContactText: body, MessageID: id,
		Seal: protocol.EncodeSeal(seal)}
	refusal := h.contactSealRefused(ctx, msg)
	if refusal == nil {
		t.Fatal("contact's pinned person key signed as this workspace's operator")
	}
	if refusal.code != protocol.ErrInvalidSeal {
		t.Fatalf("foreign operator seal refused as %q, want invalid seal", refusal.code)
	}
}

func TestSignedContactSendAcceptanceContactPersonCannotSignLocalAgentSend(t *testing.T) {
	h, contact, _, _ := signedContactSendFixture(t)
	const id, body, recipient = "foreign-person-local", "not from our operator", "local-agent"
	header := signing.Header{MessageID: id, SenderKind: signing.SenderOperator, SenderID: protocol.OperatorID,
		Recipients: []string{recipient}, TextHash: signing.TextHash(body)}
	seal := foreignContactPersonSeal(t, h, contact, header)
	msg := protocol.Message{Type: protocol.TypeMsg, MessageID: id, To: recipient,
		Payload: json.RawMessage(`{"text":"not from our operator"}`), Seal: protocol.EncodeSeal(seal)}
	refusal := h.checkSeal(context.Background(), msg, store.OperatorScope{})
	if refusal == nil {
		t.Fatal("contact's pinned person key signed as this workspace's local operator")
	}
	if refusal.code != protocol.ErrInvalidSeal {
		t.Fatalf("foreign operator seal refused as %q, want invalid seal", refusal.code)
	}
}

func foreignContactPersonSeal(t *testing.T, h *Hub, contact string, header signing.Header) signing.Seal {
	t.Helper()
	_, person, err := ed25519.GenerateKey(nil)
	messageWorkerOK(t, err)
	_, device, err := ed25519.GenerateKey(nil)
	messageWorkerOK(t, err)
	now := time.Now().UTC().Truncate(time.Millisecond)
	personPublic := person.Public().(ed25519.PublicKey)
	messageWorkerOK(t, h.store.PinSigningRoot(context.Background(), protocol.SigningRoot{
		KeyID: signing.KeyID(personPublic), Person: true, Workspace: contact, Approved: true,
	}, now))
	cert := signing.Issue(person, signing.Cert{
		Kind: signing.KindDevice, Subject: "other-operator", Key: device.Public().(ed25519.PublicKey),
		Workspace: contact, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
	})
	header.Workspace, header.CreatedAt = contact, now
	return signing.Sign(device, []signing.Cert{cert}, personPublic, header)
}
