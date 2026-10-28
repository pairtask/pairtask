//go:build linux

package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func messageIdentity(t *testing.T) *peer.WorkspaceIdentity {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "identity.db"))
	operatorNoError(t, err)
	t.Cleanup(func() { db.Close() })
	id, err := peer.LoadOrCreateIdentity(context.Background(), db.PeerDir(), db, rand.Reader)
	operatorNoError(t, err)
	return id
}
func messageConfirm(f *redeemOperatorFixture, id *peer.WorkspaceIdentity, n int) {
	f.t.Helper()
	ctx := context.Background()
	o := peer.PairingOutcome{OperationID: fmt.Sprint("message-", n), InvitationID: fmt.Sprint("invite-", n), IdentityKey: id.Fingerprint(), Name: "Contact", TranscriptID: make([]byte, 64), At: time.Now()}
	_, err := f.st.Commit(ctx, o)
	operatorNoError(f.t, err)
	_, err = f.st.Accept(ctx, id.Fingerprint())
	operatorNoError(f.t, err)
	_, err = f.st.Deliver(ctx, id.Fingerprint(), peer.PairingAccepted, time.Now())
	operatorNoError(f.t, err)
}

type messageRoute struct{ destination, pin string }

func messageConnect(t *testing.T, route messageRoute, id *peer.WorkspaceIdentity) (*firstcontact.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", route.destination)
	if err != nil {
		return nil, err
	}
	conn, err := firstcontact.MessageClient(ctx, raw, id, time.Now, route.pin)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { raw.Close(); conn.Close() })
	hello, err := messaging.EncodeHello(messaging.Hello{Min: 1, Max: 1})
	if err != nil {
		return conn, err
	}
	if err = conn.MessageFrames(ctx).Send(hello); err != nil {
		return conn, err
	}
	reply, err := conn.MessageFrames(ctx).Recv()
	if err != nil {
		return conn, err
	}
	_, err = messaging.DecodeHello(reply)
	return conn, err
}

func TestMessageAdmissionAcceptanceSaturationLeavesPairingAvailable(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	ep, err := f.h.PairingEndpoint()
	operatorNoError(t, err)
	route := messageRoute{relayToPairing(t, f), ep.Identity.Fingerprint()}
	ids := make([]*peer.WorkspaceIdentity, 5)
	for i := range ids {
		ids[i] = messageIdentity(t)
		messageConfirm(f, ids[i], i)
	}
	for i := 0; i < 4; i++ {
		_, err := messageConnect(t, route, ids[i])
		operatorNoError(t, err)
	}
	if _, err := messageConnect(t, route, ids[4]); err == nil {
		t.Fatal("fifth live message session admitted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", route.destination)
	operatorNoError(t, err)
	defer raw.Close()
	conn, err := firstcontact.Client(ctx, raw, ids[4], time.Now)
	operatorNoError(t, err)
	defer conn.Close()
	hello, err := peer.EncodeJoinerHello(peer.JoinerHello{OperationID: "pairing-still-live", Name: "New peer"})
	operatorNoError(t, err)
	operatorNoError(t, conn.Frames(ctx).Send(hello))
	// With no open invitation, the real pairing protocol still answers its
	// refusal. A TLS handshake alone would not prove post-handshake admission.
	reply, err := conn.Frames(ctx).Recv()
	operatorNoError(t, err)
	if len(reply) == 0 {
		t.Fatal("pairing produced no protocol response under message saturation")
	}
}

func TestMessageAdmissionAcceptanceOneSessionPerContact(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	ep, err := f.h.PairingEndpoint()
	operatorNoError(t, err)
	route := messageRoute{relayToPairing(t, f), ep.Identity.Fingerprint()}
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	_, err = messageConnect(t, route, id)
	operatorNoError(t, err)
	if _, err = messageConnect(t, route, id); err == nil {
		t.Fatal("same contact acquired two simultaneous sessions")
	}
	other := messageIdentity(t)
	messageConfirm(f, other, 2)
	_, err = messageConnect(t, route, other)
	operatorNoError(t, err)
}

func TestMessageAdmissionAcceptancePendingPairingCannotMessage(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	ep, err := f.h.PairingEndpoint()
	operatorNoError(t, err)
	route := messageRoute{relayToPairing(t, f), ep.Identity.Fingerprint()}
	id := messageIdentity(t)
	o := peer.PairingOutcome{OperationID: "pending", InvitationID: "invitation", IdentityKey: id.Fingerprint(), Name: "Pending", TranscriptID: make([]byte, 64), At: time.Now()}
	_, err = f.st.Commit(context.Background(), o)
	operatorNoError(t, err)
	if _, err = messageConnect(t, route, id); err == nil {
		t.Fatal("pending pairing pin admitted message session")
	}
}
