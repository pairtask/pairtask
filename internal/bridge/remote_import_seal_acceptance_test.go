package bridge

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
	"github.com/pairtask/pairtask/internal/store"
)

// This starts with a signed peer message, then uses the receiving store's
// real import and durable replay paths before the agent bridge checks it.
func TestRemoteImportSealAcceptanceAgentSeesOriginalSignatureLiveAndOnReplay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		operator bool
	}{{"operator", true}, {"agent", false}} {
		t.Run(tc.name, func(t *testing.T) { testImportedSealCase(t, tc.operator) })
	}
}

type importedSealCase struct {
	db        *store.Store
	contact   string
	own       string
	workspace ed25519.PrivateKey
	now       time.Time
}

func testImportedSealCase(t *testing.T, operator bool) {
	f := newImportedSealCase(t)
	message, root := signedImportMessage(t, signedImportArgs{operator, f.contact, f.own, f.workspace, f.now})
	if operator {
		if err := f.db.PinSigningRoot(context.Background(), root, f.now); err != nil {
			t.Fatal(err)
		}
	}
	imported := f.importMessage(t, message)
	assertSignedID(t, imported, message.ID)
	assertImportedSealVerdict(t, imported, root)
	replay := f.replay(t)
	assertSignedID(t, replay, message.ID)
	assertImportedSealVerdict(t, replay, root)
}

func newImportedSealCase(t *testing.T) importedSealCase {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "receiver.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	workspace := bridgeIdentityKey("import-sender-workspace")
	contact := signing.KeyID(workspace.Public().(ed25519.PublicKey))
	f := importedSealCase{db: db, contact: contact, workspace: workspace, now: time.Now().UTC().Truncate(time.Millisecond),
		own: signing.KeyID(bridgeIdentityKey("import-receiver-workspace").Public().(ed25519.PublicKey))}
	f.pairAndShare(t)
	return f
}

func (f importedSealCase) pairAndShare(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := f.db.InitIdentity(ctx, f.own); err != nil {
		t.Fatal(err)
	}
	_, err := f.db.Commit(ctx, peer.PairingOutcome{OperationID: "seal-import-pair", InvitationID: "seal-import-invite",
		IdentityKey: f.contact, Name: "Work Mac", TranscriptID: make([]byte, 64), At: f.now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Accept(ctx, f.contact); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Deliver(ctx, f.contact, peer.PairingAccepted, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "alpha", SessionID: "old-session"}, protocol.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.PeerMessages().Share(ctx, f.contact, []string{"alpha"}, f.now); err != nil {
		t.Fatal(err)
	}
}

func (f importedSealCase) importMessage(t *testing.T, message messaging.Message) protocol.Message {
	t.Helper()
	ctx := context.Background()
	answer, err := f.db.PeerMessages().AcceptVersion(ctx, f.contact, messaging.Arrival{
		Version: int(messaging.SealVersion), Message: message, At: f.now})
	if err != nil || !answer.Stored {
		t.Fatalf("signed peer message was not accepted: answer=%+v err=%v", answer, err)
	}
	pending, err := f.db.PendingImports(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending import: %+v err=%v", pending, err)
	}
	imported, err := f.db.ImportRemote(ctx, pending[0], 1, f.now)
	if err != nil || imported.State != store.ImportImported {
		t.Fatalf("import: %+v err=%v", imported, err)
	}
	return imported.Msg
}

func (f importedSealCase) replay(t *testing.T) protocol.Message {
	t.Helper()
	replay, err := f.db.ClaimPendingFor(context.Background(), protocol.Recipient{AgentID: "alpha", SessionID: "replacement"}, protocol.Version)
	if err != nil || len(replay) != 1 {
		t.Fatalf("durable replay: %+v err=%v", replay, err)
	}
	return replay[0]
}

func assertSignedID(t *testing.T, message protocol.Message, originalID string) {
	t.Helper()
	if message.MessageID == originalID {
		t.Fatalf("import reused the signed ID as the local delivery ID: %+v", message)
	}
	if message.Remote == nil {
		t.Fatalf("import lost the remote origin: %+v", message)
	}
	if message.Remote.MessageID != originalID {
		t.Fatalf("import lost the signed ID under local delivery ID: %+v", message)
	}
}

type signedImportArgs struct {
	operator  bool
	contact   string
	own       string
	workspace ed25519.PrivateKey
	now       time.Time
}

func signedImportMessage(t *testing.T, args signedImportArgs) (messaging.Message, protocol.SigningRoot) {
	t.Helper()
	text := "\nReport from the other workspace:\n```text\nfirst line\nsecond line\n```"
	id := "original-agent-id"
	from, kind := "remote-agent", signing.SenderAgent
	issuer, keyKind, subject := args.workspace, signing.KindBroker, "broker"
	if args.operator {
		id, from, kind = "original-operator-id", "", signing.SenderOperator
		issuer, keyKind, subject = bridgeIdentityKey("import-person"), signing.KindDevice, "operator-chat"
	}
	device := bridgeIdentityKey("import-device-" + id)
	public := issuer.Public().(ed25519.PublicKey)
	cert := signing.Issue(issuer, signing.Cert{Kind: keyKind, Subject: subject,
		Key: device.Public().(ed25519.PublicKey), Workspace: args.contact,
		NotBefore: args.now.Add(-time.Minute), NotAfter: args.now.Add(time.Hour)})
	sender := from
	if args.operator {
		sender = protocol.OperatorID
	}
	header := signing.Header{MessageID: id, SenderKind: kind, SenderID: sender, Workspace: args.contact,
		Recipients: (protocol.PeerLink{Contact: args.contact, Receiver: args.own, To: []string{"alpha"}}).Recipients(),
		CreatedAt:  args.now, TextHash: signing.TextHash(text)}
	seal := signing.Sign(device, []signing.Cert{cert}, public, header)
	root := protocol.SigningRoot{KeyID: signing.KeyID(public), Workspace: args.contact, Approved: true, Person: args.operator}
	return messaging.Message{ID: id, N: 1, CreatedAt: args.now, From: from, To: []string{"alpha"}, Text: text, Seal: seal.Marshal()}, root
}

func assertImportedSealVerdict(t *testing.T, message protocol.Message, root protocol.SigningRoot) {
	t.Helper()
	c, remote := pipeClient(t)
	c.protocolVersion, c.agentID = protocol.Version, "alpha"
	c.roots = []protocol.SigningRoot{root}
	ack := readBridgeReceipt(t, remote)
	verdict := (bridgeSignedFixture{client: c, remote: remote, message: message}).deliver(t)
	requireBridgeState(t, verdict, signing.Verified)
	assertBridgeReceipt(t, ack, message.MessageID)
	assertImportedSigner(t, verdict, root.Person)
}

type bridgeReceipt struct {
	ack <-chan protocol.Message
	err <-chan error
}

func readBridgeReceipt(t *testing.T, remote net.Conn) bridgeReceipt {
	t.Helper()
	if err := remote.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	ack := make(chan protocol.Message, 1)
	ackError := make(chan error, 1)
	go func() {
		var receipt protocol.Message
		if err := protocol.ReadFrame(bufio.NewReader(remote), &receipt); err != nil {
			ackError <- err
			return
		}
		ack <- receipt
	}()
	return bridgeReceipt{ack: ack, err: ackError}
}

func assertBridgeReceipt(t *testing.T, got bridgeReceipt, localID string) {
	t.Helper()
	select {
	case receipt := <-got.ack:
		if receipt.Type != protocol.TypeAck || receipt.MessageID != localID {
			t.Fatalf("adapter receipt lost local delivery ID: %+v", receipt)
		}
	case err := <-got.err:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("adapter receipt did not arrive")
	}
}

func assertImportedSigner(t *testing.T, verdict *protocol.Verification, person bool) {
	t.Helper()
	want := signing.BasisWorkspace
	if person {
		want = signing.BasisPerson
	}
	if verdict.Signer == nil || verdict.Signer.Basis != want {
		t.Fatalf("imported signer: %+v", verdict)
	}
}
