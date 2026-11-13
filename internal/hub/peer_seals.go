package hub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
	"github.com/pairtask/pairtask/internal/store"
)

// Broker seals (#115, phase 1a): this broker seals its agents' messages
// to paired workspaces with a key its workspace key certifies, so the
// receiving side knows the message came from this workspace unaltered,
// whoever relayed it. It attests the agent as this broker knows it: all
// agents share one Unix user, so no agent key of their own would be
// private (phase 1b).

const (
	brokerCertValidity = 30 * 24 * time.Hour
	brokerCertRenew    = 24 * time.Hour
)

// brokerKey is the key this broker seals with, held in memory only; its
// certificate is valid when each message was signed, so stored seals keep
// verifying after a restart replaces it.
type brokerKey struct {
	key  ed25519.PrivateKey
	cert signing.Cert
	root ed25519.PublicKey
}

// peerSealer seals this broker's agents' messages to paired workspaces;
// nil, and the messages go unsigned, without a workspace identity.
func (h *Hub) peerSealer(ctx context.Context) store.PeerSealer {
	if h.loadIdentity(ctx) != "" {
		return nil
	}
	now := time.Now()
	if h.broker == nil || h.broker.cert.Workspace != h.identity.Fingerprint() || now.Add(brokerCertRenew).After(h.broker.cert.NotAfter) {
		h.broker = newBrokerKey(h.identity, now)
	}
	return h.broker.seal
}

func newBrokerKey(id *peer.WorkspaceIdentity, now time.Time) *brokerKey {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic("hub: no randomness for a broker key: " + err.Error())
	}
	cert := id.Certify(signing.Cert{Kind: signing.KindBroker, Subject: "broker", Key: pub, Workspace: id.Fingerprint(),
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(brokerCertValidity)})
	return &brokerKey{key: key, cert: cert, root: id.PublicKey()}
}

// seal signs m as this workspace's agent m.From sent it to contact.
func (b *brokerKey) seal(contact string, m messaging.Message) []byte {
	h := signing.Header{MessageID: m.ID, SenderKind: signing.SenderAgent, SenderID: m.From, Workspace: b.cert.Workspace,
		Recipients: protocol.PeerSealRecipients(contact, m.To), InReplyTo: protocol.PeerReplyRef(m.ReplyTo.ID, m.ReplyTo.Author),
		CreatedAt: m.CreatedAt, TextHash: signing.TextHash(m.Text)}
	return signing.Sign(b.key, []signing.Cert{b.cert}, b.root, h).Marshal()
}

// contactSealRefused checks an operator contact send's seal, the
// operator's own, carried to the contact: it must verify here and sign
// exactly this send to that contact. A send without one goes unsigned.
func (h *Hub) contactSealRefused(ctx context.Context, msg protocol.Message) *routingError {
	if msg.Verification != nil {
		return &routingError{protocol.ErrMalformedFrame, "verification is each recipient's own and cannot be sent"}
	}
	if msg.Seal == "" {
		return nil
	}
	if msg.ContactOp != protocol.ContactOpSend {
		return &routingError{protocol.ErrMalformedFrame, "only a contact send carries a seal"}
	}
	seal, err := protocol.DecodeSeal(msg.Seal)
	if err != nil {
		return invalidSeal("unreadable seal")
	}
	roots, err := h.signingRoots(ctx)
	if err != nil {
		return &routingError{protocol.ErrInternal, "signing roots unavailable"}
	}
	if result := signing.Verify(&seal, protocol.RootsOf(roots), time.Now()); result.State != signing.Verified {
		return invalidSeal("seal is " + string(result.State) + ": " + result.Reason)
	}
	if !contactSealSigns(seal, msg) {
		return invalidSeal("the seal signs another message")
	}
	return nil
}

// contactSealSigns: the seal is the operator's for this very send.
func contactSealSigns(seal signing.Seal, msg protocol.Message) bool {
	want := protocol.PeerSealRecipients(msg.ContactKey, peerRecipients(msg))
	frame := signing.Frame{MessageID: msg.MessageID, SenderKind: signing.SenderOperator, SenderID: protocol.OperatorID,
		Text: msg.ContactText, Addresses: want}
	return slices.Equal(seal.Header.Recipients, want) && seal.Header.InReplyTo == "" && seal.Matches(frame) == nil
}

// sealBytes is a checked seal as the store keeps it; nil for none.
func sealBytes(text string) []byte {
	if text == "" {
		return nil
	}
	seal, err := protocol.DecodeSeal(text)
	if err != nil {
		return nil
	}
	return seal.Marshal()
}

// contactAcceptKey is the operator's acceptance of a contact's person key:
// one the contact signed with here, given in contact_text. Its operator
// messages verify from then on; nothing is accepted on first sight.
func (h *Hub) contactAcceptKey(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	if peer.ValidIdentityKey(msg.ContactText) != nil {
		return protocol.Message{}, &routingError{protocol.ErrMalformedFrame, "contact_text must be the person key: sha256: and 64 lowercase hex digits"}
	}
	err := h.store.AcceptPersonKey(ctx, msg.ContactKey, msg.ContactText, time.Now())
	switch {
	case errors.Is(err, store.ErrNoSuchPersonKey):
		return protocol.Message{}, &routingError{protocol.ErrContactRefused, "that contact never signed with that key here"}
	case errors.Is(err, store.ErrRootConflict):
		return protocol.Message{}, &routingError{protocol.ErrContactRefused, "that key is pinned here already for another workspace or role"}
	case err != nil:
		return protocol.Message{}, err
	}
	if err := h.store.FinishOperatorAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditAccepted}); err != nil {
		return protocol.Message{}, err
	}
	h.MessagesChanged(msg.ContactKey)
	return h.contactList(ctx, protocol.Message{MessageID: msg.MessageID, RequestID: msg.RequestID, ProtocolVersion: msg.ProtocolVersion,
		ContactKey: msg.ContactKey, ContactOp: msg.ContactOp}), nil
}
