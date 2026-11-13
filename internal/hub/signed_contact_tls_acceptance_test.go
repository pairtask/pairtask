package hub

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

type tlsSealReceipt struct {
	receiver        *Hub
	agent           *Client
	ctx             context.Context
	senderWorkspace string
	seal            []byte
	personID        string
	now             time.Time
}

func TestSignedContactTLSAcceptancePinnedLinkKeepsOperatorSeal(t *testing.T) {
	f := newAddressFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	remote := newPauseRemoteServer(t, f.h, ctx, false)
	f.key = remote.id.Fingerprint()
	addressContact(t, f.h, f.key)
	messageWorkerOK(t, remote.db.InitIdentity(ctx, f.key))
	receiver := New(remote.db, 0, slog.Default())
	receiver.identity = remote.id
	agent := remoteHubClient(t, receiver, "recipient-agent", protocol.SealVersion)
	remoteHubShare(t, receiver, f.h.identity.Fingerprint(), agent.ID)
	f.h.SetPairing(PairingConfig{Clock: time.Now, Dial: func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", remote.listener.Addr().String())
	}})

	_, person, err := ed25519.GenerateKey(nil)
	messageWorkerOK(t, err)
	_, device, err := ed25519.GenerateKey(nil)
	messageWorkerOK(t, err)
	now := time.Now().UTC().Truncate(time.Millisecond)
	personPublic := person.Public().(ed25519.PublicKey)
	senderWorkspace := f.h.identity.Fingerprint()
	cert := signing.Issue(person, signing.Cert{
		Kind: signing.KindDevice, Subject: "tls-test-operator", Key: device.Public().(ed25519.PublicKey),
		Workspace: senderWorkspace, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
	})
	const id, body = "tls-signed-contact", "signed over pinned TLS"
	header := signing.Header{
		MessageID: id, SenderKind: signing.SenderOperator, SenderID: protocol.OperatorID,
		Workspace: senderWorkspace, Recipients: (protocol.PeerLink{Receiver: f.key, To: []string{agent.ID}}).Recipients(),
		CreatedAt: now, TextHash: signing.TextHash(body),
	}
	seal := signing.Sign(device, []signing.Cert{cert}, personPublic, header).Marshal()
	_, err = f.h.store.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: f.key, ID: id, Text: body, To: []string{agent.ID}, Now: now, Seal: seal})
	messageWorkerOK(t, err)

	result, live, err := f.h.dialSession(ctx, f.contact(t))
	messageWorkerOK(t, err)
	if live == nil {
		t.Fatal("pinned TLS session did not negotiate a live link")
	}
	defer live.conn.Close()
	defer f.h.releaseLive(f.key, live.link)
	if result.Sent != 1 {
		t.Fatalf("pinned TLS session sent %d messages, want 1", result.Sent)
	}
	receipt := tlsSealReceipt{receiver: receiver, agent: agent, ctx: ctx, senderWorkspace: senderWorkspace,
		seal: seal, personID: signing.KeyID(personPublic), now: now}
	receipt.assertDelivery(t)
	receipt.assertApproval(t)
}

func (r tlsSealReceipt) assertDelivery(t *testing.T) {
	t.Helper()
	r.assertStored(t)
	r.assertImported(t)
	r.assertPending(t)
}

func (r tlsSealReceipt) assertStored(t *testing.T) {
	t.Helper()
	rows, _, err := r.receiver.store.PeerMessages().Page(r.ctx, r.senderWorkspace, 0, 10)
	messageWorkerOK(t, err)
	if len(rows) != 1 {
		t.Fatalf("pinned TLS recipient got %d rows, want 1", len(rows))
	}
	if rows[0].Direction != messaging.DirIn || !bytes.Equal(rows[0].Seal, r.seal) {
		t.Fatalf("pinned TLS recipient lost seal: %+v", rows)
	}
}

func (r tlsSealReceipt) assertImported(t *testing.T) {
	t.Helper()
	r.receiver.drainImports(r.ctx, map[string]*Client{r.agent.ID: r.agent})
	delivered := recv(t, r.agent)
	if delivered.Origin != protocol.OriginRemote || delivered.From != protocol.OperatorID {
		t.Fatalf("recipient broker/agent delivery lost provenance: %+v", delivered)
	}
	if delivered.Seal != base64.StdEncoding.EncodeToString(r.seal) {
		t.Fatalf("recipient broker/agent delivery lost provenance: %+v", delivered)
	}
}

func (r tlsSealReceipt) assertPending(t *testing.T) {
	t.Helper()
	keys, err := r.receiver.store.PeerMessages().PendingPersonKeys(r.ctx, r.senderWorkspace)
	messageWorkerOK(t, err)
	if len(keys) != 1 {
		t.Fatalf("remote operator key count=%d, want one", len(keys))
	}
	if keys[0] != r.personID {
		t.Fatalf("remote operator key was not held for approval: %v", keys)
	}
}

func (r tlsSealReceipt) assertApproval(t *testing.T) {
	t.Helper()
	messageWorkerOK(t, r.receiver.store.AcceptPersonKey(r.ctx, r.senderWorkspace, r.personID, r.now))
	roots, err := r.receiver.store.SigningRoots(r.ctx)
	messageWorkerOK(t, err)
	parsed, err := signing.ParseSeal(r.seal)
	messageWorkerOK(t, err)
	got := signing.Verify(&parsed, protocol.RootsOf(roots), time.Now())
	if got.State != signing.Verified || got.Signer == nil {
		t.Fatalf("approved TLS contact signature did not verify: %+v", got)
	}
	if got.Signer.Basis != signing.BasisPerson {
		t.Fatalf("approved TLS contact signature did not verify: %+v", got)
	}
}
