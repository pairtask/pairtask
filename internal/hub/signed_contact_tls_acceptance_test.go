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
	rows, _, err := remote.db.PeerMessages().Page(ctx, senderWorkspace, 0, 10)
	messageWorkerOK(t, err)
	if len(rows) != 1 || rows[0].Direction != messaging.DirIn || !bytes.Equal(rows[0].Seal, seal) {
		t.Fatalf("pinned TLS recipient lost seal: %+v", rows)
	}
	receiver.drainImports(ctx, map[string]*Client{agent.ID: agent})
	delivered := recv(t, agent)
	if delivered.Origin != protocol.OriginRemote || delivered.From != protocol.OperatorID || delivered.Seal != base64.StdEncoding.EncodeToString(seal) {
		t.Fatalf("recipient broker/agent delivery lost provenance: %+v", delivered)
	}
	personID := signing.KeyID(personPublic)
	keys, err := remote.db.PeerMessages().PendingPersonKeys(ctx, senderWorkspace)
	messageWorkerOK(t, err)
	if len(keys) != 1 || keys[0] != personID {
		t.Fatalf("remote operator key was not held for approval: %v", keys)
	}
	messageWorkerOK(t, remote.db.AcceptPersonKey(ctx, senderWorkspace, personID, now))
	roots, err := remote.db.SigningRoots(ctx)
	messageWorkerOK(t, err)
	parsed, err := signing.ParseSeal(rows[0].Seal)
	messageWorkerOK(t, err)
	if got := signing.Verify(&parsed, protocol.RootsOf(roots), time.Now()); got.State != signing.Verified || got.Signer == nil || got.Signer.Basis != signing.BasisPerson {
		t.Fatalf("approved TLS contact signature did not verify: %+v", got)
	}
}
