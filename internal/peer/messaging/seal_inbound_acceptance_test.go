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

func requireInboundStored(t *testing.T, answer messaging.Answer, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if !answer.Stored {
		t.Fatalf("signed message was not stored: %+v", answer)
	}
}

func requireInboundRefusal(t *testing.T, answer messaging.Answer, err error, reason string) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if answer.Stored || answer.Reason != reason {
		t.Fatalf("message was not refused for %q: %+v", reason, answer)
	}
}

func requirePendingPersonKey(t *testing.T, db *store.Store, want string) {
	t.Helper()
	keys, err := db.PeerMessages().PendingPersonKeys(context.Background(), sessionContact)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("pending person keys=%v, want one", keys)
	}
	if keys[0] != want {
		t.Fatalf("pending person key=%s, want %s", keys[0], want)
	}
}

func inboundPage(t *testing.T, db *store.Store, count int) []messaging.Row {
	t.Helper()
	page, _, err := db.PeerMessages().Page(context.Background(), sessionContact, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != count {
		t.Fatalf("inbound page has %d rows, want %d: %+v", len(page), count, page)
	}
	return page
}

func verifyInboundSeal(t *testing.T, db *store.Store, raw []byte) signing.Result {
	t.Helper()
	seal, err := signing.ParseSeal(raw)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := db.SigningRoots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return signing.Verify(&seal, protocol.RootsOf(roots), time.Now())
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
	requireInboundStored(t, a, err)
	requirePendingPersonKey(t, db, keyID)
	page := inboundPage(t, db, 1)
	if !bytes.Equal(page[0].Seal, m.Seal) {
		t.Fatalf("inbound seal not persisted: %+v", page[0])
	}
	if got := verifyInboundSeal(t, db, page[0].Seal); got.State != signing.Unverified {
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
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("approved person key remained pending: %v", keys)
	}
	got := verifyInboundSeal(t, db, rawSeal)
	if got.State != signing.Verified || got.Signer == nil {
		t.Fatalf("approved contact person not verified: %+v", got)
	}
	if got.Signer.Basis != signing.BasisPerson {
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
	requireInboundStored(t, a, err)
	requirePendingPersonKey(t, db, changedID)
	if got := verifyInboundSeal(t, db, changed.Seal); got.State != signing.Unverified {
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
			requireInboundRefusal(t, a, err, messaging.ReasonInvalidSeal)
			inboundPage(t, db, 0)
		})
	}
}

func TestSealInboundAcceptanceOtherWorkspaceSealIsRefused(t *testing.T) {
	// A fully valid seal for another workspace still cannot enter this contact.
	db := sealInboundFixture(t)
	m, _ := sealOperatorMessage(t, sealBaseInbound(), sealSigner{"other-person", "other-device", sealReceiver})
	a, err := db.PeerMessages().Accept(context.Background(), sessionContact, m, sessionAt)
	requireInboundRefusal(t, a, err, messaging.ReasonInvalidSeal)
}

func TestSealInboundAcceptanceChangedSealConflictsOnReplay(t *testing.T) {
	db := sealInboundFixture(t)
	ctx := context.Background()
	base := sealBaseInbound()
	first, _ := sealOperatorMessage(t, base, sealSigner{"replay-person", "first-device", sessionContact})
	a, err := db.PeerMessages().Accept(ctx, sessionContact, first, sessionAt)
	requireInboundStored(t, a, err)
	second, _ := sealOperatorMessage(t, base, sealSigner{"replay-person", "second-device", sessionContact})
	if bytes.Equal(first.Seal, second.Seal) {
		t.Fatal("fixture produced the same seal twice")
	}
	a, err = db.PeerMessages().Accept(ctx, sessionContact, second, sessionAt)
	requireInboundRefusal(t, a, err, messaging.ReasonConflict)
	page := inboundPage(t, db, 1)
	if !bytes.Equal(page[0].Seal, first.Seal) {
		t.Fatalf("replay replaced original seal: %+v", page[0])
	}
}

func TestSealInboundAcceptanceUnsignedHistoryDoesNotGainSealOnReplay(t *testing.T) {
	db := sealInboundFixture(t)
	ctx := context.Background()
	unsigned := sealBaseInbound()
	a, err := db.PeerMessages().Accept(ctx, sessionContact, unsigned, sessionAt)
	requireInboundStored(t, a, err)
	signed, _ := sealOperatorMessage(t, unsigned, sealSigner{"late-person", "late-device", sessionContact})
	a, err = db.PeerMessages().Accept(ctx, sessionContact, signed, sessionAt)
	requireInboundStored(t, a, err)
	page := inboundPage(t, db, 1)
	if len(page[0].Seal) != 0 {
		t.Fatalf("old unsigned row silently gained a seal: %+v", page[0])
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
	m := sealBaseInbound()
	m.ID, m.N = fmt.Sprintf("unsigned-v%d", messaging.SealVersion), uint64(messaging.SealVersion)
	a, err := db.PeerMessages().AcceptVersion(ctx, sessionContact, messaging.Arrival{Version: int(messaging.SealVersion), Message: m, At: sessionAt})
	requireInboundRefusal(t, a, err, messaging.ReasonUnsigned)
	m.ID, m.N = fmt.Sprintf("unsigned-v%d", messaging.PairingVersion), uint64(messaging.PairingVersion)
	a, err = db.PeerMessages().AcceptVersion(ctx, sessionContact, messaging.Arrival{Version: int(messaging.PairingVersion), Message: m, At: sessionAt})
	requireInboundStored(t, a, err)
	if row := inboundPage(t, db, 2)[1]; len(row.Seal) != 0 {
		t.Fatalf("legacy message was stored as signed: %+v", row)
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
