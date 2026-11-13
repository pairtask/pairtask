package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

// Sealing (#115): the operator's own clients sign each message with a
// device key this account's person key certified. The person key is
// registered with the broker over the operator socket, the operator's
// approval of it; no agent and no broker can then speak as the operator.

// sealer seals an operator_send.
type sealer func(protocol.Message) string

// errUnsignedHint ends a signing failure: sending without the operator's
// signature is the operator's explicit choice.
const errUnsignedHint = "; pass --unsigned to send without your signature"

// operatorSealer loads this account's keys, registers the person key with
// the broker on socket (the operator's own approval of it) and returns a
// sealer for that workspace. With no keys yet it is personkey.ErrNoKeys;
// any other failure means the operator's messages can't be signed.
func operatorSealer(ctx context.Context, socket string) (sealer, error) {
	dir, err := personkey.ResolveDir()
	if err != nil {
		return nil, err
	}
	keys, err := personkey.Load(dir)
	if err != nil {
		return nil, err
	}
	view, err := registerPerson(ctx, socket, keys)
	if err != nil {
		return nil, err
	}
	return func(msg protocol.Message) string {
		return protocol.EncodeSeal(keys.Seal(sealHeader(msg, view.Workspace, time.Now())))
	}, nil
}

// registerPerson pins keys' person key with the broker on socket.
func registerPerson(ctx context.Context, socket string, keys *personkey.Keys) (protocol.IdentityView, error) {
	resp, err := sendOperator(ctx, socket, protocol.Message{Type: protocol.TypeOperatorIdentity, MessageID: uuid.NewString(),
		IdentityOp: protocol.IdentityOpRegister, IdentityKey: base64.StdEncoding.EncodeToString(keys.PersonPublic())})
	if err != nil {
		return protocol.IdentityView{}, err
	}
	if resp.Type != protocol.TypeOperatorIdentityResult || resp.Identity == nil {
		return protocol.IdentityView{}, errors.New("the broker didn't register the person key: " + resp.Code + " " + resp.Detail)
	}
	return *resp.Identity, nil
}

// sealedSend seals a one-shot send. With no keys yet it goes unsigned, as
// before signing existed; with keys, a failure to sign stops the send
// unless unsigned: a broker refusing the key must not quietly strip the
// operator's signature.
func sealedSend(ctx context.Context, socket string, req protocol.Message, unsigned bool) (protocol.Message, error) {
	if unsigned {
		return req, nil
	}
	seal, err := operatorSealer(ctx, socket)
	switch {
	case errors.Is(err, personkey.ErrNoKeys):
		return req, nil
	case err != nil:
		return req, fmt.Errorf("can't sign: %w%s", err, errUnsignedHint)
	}
	req.Seal = seal(req)
	return req, nil
}

// sealHeader is what an operator message's seal signs: an operator_send
// here, or a contact send to a paired workspace's operator or agents.
func sealHeader(msg protocol.Message, workspace string, at time.Time) signing.Header {
	h := signing.Header{MessageID: msg.MessageID, SenderKind: signing.SenderOperator, SenderID: protocol.OperatorID, Workspace: workspace, CreatedAt: at}
	if msg.Type == protocol.TypeOperatorContact {
		h.Recipients, h.TextHash = protocol.PeerLink{Receiver: msg.ContactKey, To: msg.ContactTo}.Recipients(), signing.TextHash(msg.ContactText)
		return h
	}
	var body struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(msg.Payload, &body)
	h.Recipients, h.InReplyTo, h.TextHash = protocol.SealRecipients(msg.To, msg.Group, msg.Room), msg.InReplyTo, signing.TextHash(body.Text)
	return h
}
