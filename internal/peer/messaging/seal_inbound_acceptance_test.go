package messaging_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
	"github.com/pairtask/pairtask/internal/store"
)

const sealReceiver = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func sealTestKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte(label))
	return ed25519.NewKeyFromSeed(seed[:])
}

type sealSigner struct{ person, device, workspace string }

func sealOperatorMessage(t *testing.T, m messaging.Message, signer sealSigner) (messaging.Message, string) {
	t.Helper()
	person, device := sealTestKey(signer.person), sealTestKey(signer.device)
	personPublic := person.Public().(ed25519.PublicKey)
	cert := signing.Issue(person, signing.Cert{
		Kind: signing.KindDevice, Subject: signer.device,
		Key: device.Public().(ed25519.PublicKey), Workspace: signer.workspace,
		NotBefore: sessionAt.Add(-time.Hour), NotAfter: sessionAt.Add(time.Hour),
	})
	header := signing.Header{
		MessageID: m.ID, SenderKind: signing.SenderOperator, SenderID: protocol.OperatorID,
		Workspace: signer.workspace, Recipients: (protocol.PeerLink{Receiver: sealReceiver, To: m.To}).Recipients(),
		InReplyTo: (protocol.PeerLink{ReplyID: m.ReplyTo.ID, ReplyAuthor: m.ReplyTo.Author}).ReplyRef(),
		CreatedAt: sessionAt, TextHash: signing.TextHash(m.Text),
	}
	m.Seal = signing.Sign(device, []signing.Cert{cert}, personPublic, header).Marshal()
	return m, signing.KeyID(personPublic)
}

func sealInboundFixture(t *testing.T) *store.Store {
	t.Helper()
	db, _ := sessionFixture(t)
	if err := db.InitIdentity(context.Background(), sealReceiver); err != nil {
		t.Fatal(err)
	}
	return db
}

func sealBaseInbound() messaging.Message {
	return messaging.Message{ID: "contact-signed", N: 1, CreatedAt: sessionAt, Text: "message from their operator"}
}

func TestSealInboundAcceptancePendingPersonKeyNeedsApproval(t *testing.T) {
	db := sealInboundFixture(t)
	m, keyID := sealOperatorMessage(t, sealBaseInbound(), sealSigner{"contact-person", "contact-device", sessionContact})
	acceptPendingPersonMessage(t, db, m, keyID)
	approvePersonAndVerify(t, db, m.Seal, keyID)
	assertSecondPersonNeedsApproval(t, db)
}

func acceptPendingPersonMessage(t *testing.T, db *store.Store, m messaging.Message, keyID string) {
	t.Helper()
	ctx := context.Background()
	a, err := db.PeerMessages().Accept(ctx, sessionContact, m, sessionAt)
	if err != nil || !a.Stored {
		t.Fatalf("unapproved valid signature should be stored as unverified: answer=%+v err=%v", a, err)
	}
	keys, err := db.PeerMessages().PendingPersonKeys(ctx, sessionContact)
	if err != nil || len(keys) != 1 || keys[0] != keyID {
		t.Fatalf("new contact person key not pending: keys=%v err=%v", keys, err)
	}
	page, _, err := db.PeerMessages().Page(ctx, sessionContact, 0, 10)
	if err != nil || len(page) != 1 || !bytes.Equal(page[0].Seal, m.Seal) {
		t.Fatalf("inbound seal not persisted: page=%+v err=%v", page, err)
	}
	seal, err := signing.ParseSeal(page[0].Seal)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := db.SigningRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := signing.Verify(&seal, protocol.RootsOf(roots), time.Now()); got.State != signing.Unverified {
		t.Fatalf("unapproved person key gained authority: %+v", got)
	}
}

func approvePersonAndVerify(t *testing.T, db *store.Store, rawSeal []byte, keyID string) {
	t.Helper()
	ctx := context.Background()
	if err := db.AcceptPersonKey(ctx, sessionContact, keyID, sessionAt); err != nil {
		t.Fatal(err)
	}
	keys, err := db.PeerMessages().PendingPersonKeys(ctx, sessionContact)
	if err != nil || len(keys) != 0 {
		t.Fatalf("approved person key remained pending: keys=%v err=%v", keys, err)
	}
	roots, err := db.SigningRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seal, err := signing.ParseSeal(rawSeal)
	if err != nil {
		t.Fatal(err)
	}
	if got := signing.Verify(&seal, protocol.RootsOf(roots), time.Now()); got.State != signing.Verified || got.Signer == nil || got.Signer.Basis != signing.BasisPerson {
		t.Fatalf("approved contact person not verified: %+v", got)
	}
}

func assertSecondPersonNeedsApproval(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	changed := sealBaseInbound()
	changed.ID, changed.N = "contact-new-person-key", 2
	changed, changedID := sealOperatorMessage(t, changed, sealSigner{"contact-new-person", "contact-new-device", sessionContact})
	a, err := db.PeerMessages().Accept(ctx, sessionContact, changed, sessionAt)
	if err != nil || !a.Stored {
		t.Fatalf("new person key's valid message was not retained: answer=%+v err=%v", a, err)
	}
	keys, err := db.PeerMessages().PendingPersonKeys(ctx, sessionContact)
	if err != nil || len(keys) != 1 || keys[0] != changedID {
		t.Fatalf("new person key silently inherited first approval: keys=%v err=%v", keys, err)
	}
	secondSeal, err := signing.ParseSeal(changed.Seal)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := db.SigningRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := signing.Verify(&secondSeal, protocol.RootsOf(roots), time.Now()); got.State != signing.Unverified {
		t.Fatalf("second person key gained authority before approval: %+v", got)
	}
}

func TestSealInboundAcceptanceTamperingIsRefusedBeforePersistence(t *testing.T) {
	changes := map[string]func(*messaging.Message){
		"text":       func(m *messaging.Message) { m.Text = "altered by relay" },
		"sender":     func(m *messaging.Message) { m.From = "forged-agent" },
		"recipients": func(m *messaging.Message) { m.To = []string{"other-agent"} },
		"reply direction": func(m *messaging.Message) {
			m.ReplyTo = messaging.ReplyRef{ID: "colliding-parent", Author: messaging.ReplyByReceiver}
		},
		"signature bytes": func(m *messaging.Message) { m.Seal[len(m.Seal)-1] ^= 1 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			db := sealInboundFixture(t)
			m, _ := sealOperatorMessage(t, sealBaseInbound(), sealSigner{"tamper-person", "tamper-device", sessionContact})
			change(&m)
			a, err := db.PeerMessages().Accept(context.Background(), sessionContact, m, sessionAt)
			if err != nil || a.Stored || a.Reason != messaging.ReasonInvalidSeal {
				t.Fatalf("tampered signed message: answer=%+v err=%v", a, err)
			}
			page, _, err := db.PeerMessages().Page(context.Background(), sessionContact, 0, 10)
			if err != nil || len(page) != 0 {
				t.Fatalf("tampered message persisted: page=%+v err=%v", page, err)
			}
		})
	}
	// A fully valid seal for another workspace still cannot enter this contact.
	db := sealInboundFixture(t)
	m, _ := sealOperatorMessage(t, sealBaseInbound(), sealSigner{"other-person", "other-device", sealReceiver})
	a, err := db.PeerMessages().Accept(context.Background(), sessionContact, m, sessionAt)
	if err != nil || a.Stored || a.Reason != messaging.ReasonInvalidSeal {
		t.Fatalf("another workspace's signed message: answer=%+v err=%v", a, err)
	}
}

func TestSealInboundAcceptanceChangedSealConflictsOnReplay(t *testing.T) {
	db := sealInboundFixture(t)
	ctx := context.Background()
	base := sealBaseInbound()
	first, _ := sealOperatorMessage(t, base, sealSigner{"replay-person", "first-device", sessionContact})
	a, err := db.PeerMessages().Accept(ctx, sessionContact, first, sessionAt)
	if err != nil || !a.Stored {
		t.Fatalf("first arrival: answer=%+v err=%v", a, err)
	}
	second, _ := sealOperatorMessage(t, base, sealSigner{"replay-person", "second-device", sessionContact})
	if bytes.Equal(first.Seal, second.Seal) {
		t.Fatal("fixture produced the same seal twice")
	}
	a, err = db.PeerMessages().Accept(ctx, sessionContact, second, sessionAt)
	if err != nil || a.Stored || a.Reason != messaging.ReasonConflict {
		t.Fatalf("changed seal replay: answer=%+v err=%v", a, err)
	}
	page, _, err := db.PeerMessages().Page(ctx, sessionContact, 0, 10)
	if err != nil || len(page) != 1 || !bytes.Equal(page[0].Seal, first.Seal) {
		t.Fatalf("replay replaced original seal: page=%+v err=%v", page, err)
	}
}

func TestSealInboundAcceptanceUnsignedHistoryDoesNotGainSealOnReplay(t *testing.T) {
	db := sealInboundFixture(t)
	ctx := context.Background()
	unsigned := sealBaseInbound()
	a, err := db.PeerMessages().Accept(ctx, sessionContact, unsigned, sessionAt)
	if err != nil || !a.Stored {
		t.Fatalf("old unsigned arrival: answer=%+v err=%v", a, err)
	}
	signed, _ := sealOperatorMessage(t, unsigned, sealSigner{"late-person", "late-device", sessionContact})
	a, err = db.PeerMessages().Accept(ctx, sessionContact, signed, sessionAt)
	if err != nil || !a.Stored {
		t.Fatalf("same-message signed retry: answer=%+v err=%v", a, err)
	}
	page, _, err := db.PeerMessages().Page(ctx, sessionContact, 0, 10)
	if err != nil || len(page) != 1 || len(page[0].Seal) != 0 {
		t.Fatalf("old unsigned row silently gained a seal: page=%+v err=%v", page, err)
	}
}

func TestSealInboundAcceptanceRequireSealsIsOptInAndVersionAware(t *testing.T) {
	db := sealInboundFixture(t)
	assertDefaultUnsignedAccepted(t, db)
	assertRequiredUnsignedRefused(t, db)
	assertRequiredSignedAccepted(t, db)
	assertUnsignedAcceptedAfterRelaxingPolicy(t, db)
}

func assertDefaultUnsignedAccepted(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	required, err := db.PeerMessages().RequiresSeals(ctx, sessionContact)
	if err != nil || required {
		t.Fatalf("fresh contact requires seals by default: required=%t err=%v", required, err)
	}
	old := sealBaseInbound()
	a, err := db.PeerMessages().AcceptVersion(ctx, sessionContact, messaging.Arrival{Version: int(messaging.SealVersion), Message: old, At: sessionAt})
	if err != nil || !a.Stored {
		t.Fatalf("default unsigned migration was refused: answer=%+v err=%v", a, err)
	}
}

func assertRequiredUnsignedRefused(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := db.SetRequireSeals(ctx, sessionContact, true, sessionAt); err != nil {
		t.Fatal(err)
	}
	required, err := db.PeerMessages().RequiresSeals(ctx, sessionContact)
	if err != nil || !required {
		t.Fatalf("require-seals choice was not stored: required=%t err=%v", required, err)
	}
	for _, tc := range []struct {
		version int
		reason  string
	}{
		{int(messaging.SealVersion), messaging.ReasonUnsigned},
		{int(messaging.PairingVersion), messaging.ReasonInvalid},
	} {
		m := sealBaseInbound()
		m.ID, m.N = fmt.Sprintf("unsigned-v%d", tc.version), uint64(tc.version)
		a, err := db.PeerMessages().AcceptVersion(ctx, sessionContact, messaging.Arrival{Version: tc.version, Message: m, At: sessionAt})
		if err != nil || a.Stored || a.Reason != tc.reason {
			t.Fatalf("version %d unsigned policy: answer=%+v err=%v", tc.version, a, err)
		}
	}
}

func assertRequiredSignedAccepted(t *testing.T, db *store.Store) {
	t.Helper()
	signed := sealBaseInbound()
	signed.ID, signed.N = "signed-after-require", 20
	signed, _ = sealOperatorMessage(t, signed, sealSigner{"required-person", "required-device", sessionContact})
	a, err := db.PeerMessages().AcceptVersion(context.Background(), sessionContact, messaging.Arrival{Version: int(messaging.SealVersion), Message: signed, At: sessionAt})
	if err != nil || !a.Stored {
		t.Fatalf("valid signature refused after require: answer=%+v err=%v", a, err)
	}
}

func assertUnsignedAcceptedAfterRelaxingPolicy(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := db.SetRequireSeals(ctx, sessionContact, false, sessionAt); err != nil {
		t.Fatal(err)
	}
	unsignedAgain := sealBaseInbound()
	unsignedAgain.ID, unsignedAgain.N = "unsigned-after-off", 21
	a, err := db.PeerMessages().AcceptVersion(ctx, sessionContact, messaging.Arrival{Version: int(messaging.SealVersion), Message: unsignedAgain, At: sessionAt})
	if err != nil || !a.Stored {
		t.Fatalf("turning policy off did not restore unsigned acceptance: answer=%+v err=%v", a, err)
	}
}
