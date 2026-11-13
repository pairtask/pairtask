package hub

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
	"github.com/pairtask/pairtask/internal/store"
)

// Signed messages (#115), phase 1a. The operator's client seals each
// message with a device key its person key certified; the broker refuses a
// seal that doesn't verify here or doesn't sign exactly the message it
// came with, keeps the seal, and every recipient verifies it again itself.

// signingRoots are this workspace's own key, once it has one (read from
// the identity marker, so asking never creates a key), then the person
// keys its operator registered.
func (h *Hub) signingRoots(ctx context.Context) ([]protocol.SigningRoot, error) {
	pinned, err := h.store.SigningRoots(ctx)
	if err != nil {
		return nil, err
	}
	own, ok, err := h.store.IdentityFingerprint(ctx)
	if err != nil || !ok {
		return pinned, err
	}
	return append([]protocol.SigningRoot{{KeyID: own, Workspace: own, Approved: true}}, pinned...), nil
}

// operatorIdentity registers the operator's person key, the operator's
// own approval of it over the authenticated socket, or reads the roots.
func (h *Hub) operatorIdentity(ctx context.Context, _ map[string]*Client, req operatorRequest) protocol.Message {
	msg := req.msg
	if problem := h.loadIdentity(ctx); problem != "" {
		return (&routingError{protocol.ErrInvalidState, "workspace identity unavailable: " + problem}).frame(msg.MessageID)
	}
	if rerr := h.identityOp(ctx, msg); rerr != nil {
		return rerr.frame(msg.MessageID)
	}
	roots, err := h.signingRoots(ctx)
	if err != nil {
		return (&routingError{protocol.ErrInternal, "signing roots unavailable"}).frame(msg.MessageID)
	}
	view := &protocol.IdentityView{Workspace: h.identity.Fingerprint(), Roots: roots}
	return protocol.Message{Type: protocol.TypeOperatorIdentityResult, MessageID: msg.MessageID, Identity: view}
}

// identityOp carries out register; status only reads.
func (h *Hub) identityOp(ctx context.Context, msg protocol.Message) *routingError {
	switch msg.IdentityOp {
	case protocol.IdentityOpStatus:
		return nil
	case protocol.IdentityOpRegister:
		key, err := base64.StdEncoding.DecodeString(msg.IdentityKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return &routingError{protocol.ErrMalformedFrame, "identity_key must be an Ed25519 public key, base64"}
		}
		own := h.identity.Fingerprint()
		root := protocol.SigningRoot{KeyID: signing.KeyID(ed25519.PublicKey(key)), Person: true, Workspace: own, Approved: true}
		if err := h.store.PinSigningRoot(ctx, root, time.Now()); errors.Is(err, store.ErrRootConflict) {
			return &routingError{protocol.ErrInvalidState, "that key is pinned here already as another kind of key"}
		} else if err != nil {
			return &routingError{protocol.ErrInternal, "person key not pinned"}
		}
		return nil
	}
	return &routingError{protocol.ErrMalformedFrame, "identity_op must be register or status"}
}

// checkSeal refuses an operator message whose seal doesn't verify here or
// doesn't sign exactly this message: its ID, the operator, its text, its
// thread, and its recipients and room as the broker scoped them. An
// unsigned message passes; its recipients mark it unsigned.
func (h *Hub) checkSeal(ctx context.Context, msg protocol.Message, scope store.OperatorScope) *routingError {
	if msg.Verification != nil {
		return &routingError{protocol.ErrMalformedFrame, "verification is each recipient's own and cannot be sent"}
	}
	if msg.Seal == "" {
		return nil
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
	return sealSigns(seal, msg, scope)
}

// sealSigns checks that the seal signed this very message as scoped.
func sealSigns(seal signing.Seal, msg protocol.Message, scope store.OperatorScope) *routingError {
	text, ok := payloadText(msg.Payload)
	frame := signing.Frame{MessageID: msg.MessageID, SenderKind: signing.SenderOperator, SenderID: protocol.OperatorID,
		Text: text, Addresses: seal.Header.Recipients, Room: msg.Room}
	want := protocol.SealRecipients(msg.To, scope.Members, msg.Room) // To is "" for a group, "*" for everyone
	switch {
	case !ok:
		return invalidSeal("a sealed message's payload is {\"text\": …}")
	case !slices.Equal(seal.Header.Recipients, want), seal.Header.InReplyTo != msg.InReplyTo, seal.Matches(frame) != nil:
		return invalidSeal("the seal signs another message")
	}
	return nil
}

func invalidSeal(detail string) *routingError {
	return &routingError{protocol.ErrInvalidSeal, detail}
}

// checkNoSeal: in phase 1a agents don't sign, and no sender sets a
// recipient's verification.
func checkNoSeal(msg protocol.Message) *routingError {
	if msg.Seal != "" || msg.Verification != nil {
		return &routingError{protocol.ErrMalformedFrame, "seal and verification can't be sent by an agent"}
	}
	return nil
}

// welcomeRoots gives a welcome the signing roots, for a client that
// verifies seals.
func (h *Hub) welcomeRoots(ctx context.Context, c *Client, welcome protocol.Message) protocol.Message {
	if c.ProtocolVersion < protocol.SealVersion {
		return welcome
	}
	roots, err := h.signingRoots(ctx)
	if err != nil {
		h.log.Error("signing roots unavailable; welcome carries none", "agent_id", c.ID, "error", err)
	}
	welcome.SigningRoots = roots
	return welcome
}
