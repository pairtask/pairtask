package signing_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/signing"
)

var signedNow = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func signedKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte(label))
	return ed25519.NewKeyFromSeed(seed[:])
}

func signedPublic(key ed25519.PrivateKey) ed25519.PublicKey {
	return key.Public().(ed25519.PublicKey)
}

func signedCert(issuer, subject ed25519.PrivateKey, kind signing.Kind, name, workspace string) signing.Cert {
	return signing.Issue(issuer, signing.Cert{
		Kind: kind, Subject: name, Key: signedPublic(subject), Workspace: workspace,
		NotBefore: signedNow.Add(-time.Hour), NotAfter: signedNow.Add(time.Hour),
	})
}

func signedHeader(workspace string) signing.Header {
	return signing.Header{
		MessageID: "request-42", SenderKind: signing.SenderOperator, SenderID: signing.OperatorID,
		Workspace: workspace, Recipients: []string{"agent-b@receiver", "agent-a@receiver"},
		InReplyTo: "question-17", CreatedAt: signedNow, TextHash: signing.TextHash("approve"),
	}
}

func signedRoots(root ed25519.PrivateKey, workspace string, person, approved bool) signing.Roots {
	id := signing.KeyID(signedPublic(root))
	return func(requested string) (signing.Root, bool) {
		if requested != id {
			return signing.Root{}, false
		}
		return signing.Root{Workspace: workspace, Person: person, Approved: approved}, true
	}
}

func TestSignedIdentityAcceptanceOnlyPersonDeviceGrantsOperatorAuthority(t *testing.T) {
	workspaceRoot, personRoot := signedKey("workspace"), signedKey("person")
	device, broker := signedKey("device"), signedKey("broker")
	workspace := signing.KeyID(signedPublic(workspaceRoot))
	h := signedHeader(workspace)
	deviceCert := signedCert(personRoot, device, signing.KindDevice, "operator-chat", workspace)
	personSeal := signing.Sign(device, []signing.Cert{deviceCert}, signedPublic(personRoot), h)
	person := signing.Verify(&personSeal, signedRoots(personRoot, workspace, true, true), signedNow)
	if person.State != signing.Verified || person.Signer == nil || person.Signer.Basis != signing.BasisPerson || person.Signer.Kind != signing.SenderOperator {
		t.Fatalf("person-certified operator message: %+v", person)
	}

	// A paired workspace may attest agents; it cannot confer operator authority.
	brokerCert := signedCert(workspaceRoot, broker, signing.KindBroker, "broker", workspace)
	brokerSeal := signing.Sign(broker, []signing.Cert{brokerCert}, signedPublic(workspaceRoot), h)
	forged := signing.Verify(&brokerSeal, signedRoots(workspaceRoot, workspace, false, true), signedNow)
	if forged.State != signing.Invalid {
		t.Fatalf("broker key forged operator authority: %+v", forged)
	}

	wrongID := h
	wrongID.SenderID = "other-operator"
	wrongIDSeal := signing.Sign(device, []signing.Cert{deviceCert}, signedPublic(personRoot), wrongID)
	if got := signing.Verify(&wrongIDSeal, signedRoots(personRoot, workspace, true, true), signedNow); got.State != signing.Invalid {
		t.Fatalf("device key certified an unreserved operator ID: %+v", got)
	}
}

func TestSignedIdentityAcceptanceAgentAttestationNamesItsTrustBasis(t *testing.T) {
	workspaceRoot, broker, agent := signedKey("workspace-agent"), signedKey("broker-agent"), signedKey("agent-a")
	workspace := signing.KeyID(signedPublic(workspaceRoot))
	h := signedHeader(workspace)
	h.SenderKind, h.SenderID = signing.SenderAgent, "agent-a"
	trusted := signedRoots(workspaceRoot, workspace, false, true)

	brokerCert := signedCert(workspaceRoot, broker, signing.KindBroker, "broker", workspace)
	brokerSeal := signing.Sign(broker, []signing.Cert{brokerCert}, signedPublic(workspaceRoot), h)
	brokerResult := signing.Verify(&brokerSeal, trusted, signedNow)
	if brokerResult.State != signing.Verified || brokerResult.Signer == nil || brokerResult.Signer.Basis != signing.BasisWorkspace {
		t.Fatalf("broker attestation misrepresented: %+v", brokerResult)
	}

	agentCert := signedCert(workspaceRoot, agent, signing.KindAgent, "agent-a", workspace)
	agentSeal := signing.Sign(agent, []signing.Cert{agentCert}, signedPublic(workspaceRoot), h)
	agentResult := signing.Verify(&agentSeal, trusted, signedNow)
	if agentResult.State != signing.Verified || agentResult.Signer == nil || agentResult.Signer.Basis != signing.BasisAgent {
		t.Fatalf("isolated agent key not identified: %+v", agentResult)
	}
	impersonation := h
	impersonation.SenderID = "agent-b"
	badSeal := signing.Sign(agent, []signing.Cert{agentCert}, signedPublic(workspaceRoot), impersonation)
	if got := signing.Verify(&badSeal, trusted, signedNow); got.State != signing.Invalid {
		t.Fatalf("agent key impersonated another agent: %+v", got)
	}
}

func TestSignedIdentityAcceptanceApprovalAndTransportTampering(t *testing.T) {
	person, device := signedKey("person-approval"), signedKey("device-approval")
	workspace := signing.KeyID(signedPublic(signedKey("workspace-approval")))
	h := signedHeader(workspace)
	cert := signedCert(person, device, signing.KindDevice, "chat", workspace)
	seal := signing.Sign(device, []signing.Cert{cert}, signedPublic(person), h)
	if got := signing.Verify(nil, signedRoots(person, workspace, true, true), signedNow); got.State != signing.Unsigned {
		t.Fatalf("unsigned message: %+v", got)
	}
	if got := signing.Verify(&seal, signedRoots(person, workspace, true, false), signedNow); got.State != signing.Unverified {
		t.Fatalf("pending person key gained authority: %+v", got)
	}
	unknown := func(string) (signing.Root, bool) { return signing.Root{}, false }
	if got := signing.Verify(&seal, unknown, signedNow); got.State != signing.Unverified || got.Signer == nil {
		t.Fatalf("valid but unpinned person key was misclassified: %+v", got)
	}
	badCert := seal
	badCert.Chain = append([]signing.Cert(nil), seal.Chain...)
	badCert.Chain[0].Sig = append([]byte(nil), seal.Chain[0].Sig...)
	badCert.Chain[0].Sig[0] ^= 1
	if got := signing.Verify(&badCert, unknown, signedNow); got.State != signing.Invalid {
		t.Fatalf("forged certificate without a pin was called unverified: %+v", got)
	}
	if got := signing.Verify(&seal, signedRoots(person, workspace, true, true), signedNow.Add(2*time.Hour)); got.State != signing.Verified {
		t.Fatalf("durable message lost provenance after certificate expiry: %+v", got)
	}
	expiredHeader := h
	expiredHeader.CreatedAt = signedNow.Add(2 * time.Hour)
	expiredSeal := signing.Sign(device, []signing.Cert{cert}, signedPublic(person), expiredHeader)
	if got := signing.Verify(&expiredSeal, signedRoots(person, workspace, true, true), expiredHeader.CreatedAt); got.State != signing.Invalid {
		t.Fatalf("message signed outside certificate validity gained authority: %+v", got)
	}
	futureHeader := h
	futureHeader.CreatedAt = signedNow.Add(10 * time.Minute)
	futureSeal := signing.Sign(device, []signing.Cert{cert}, signedPublic(person), futureHeader)
	if got := signing.Verify(&futureSeal, signedRoots(person, workspace, true, true), signedNow); got.State != signing.Invalid {
		t.Fatalf("message signed too far in the future gained authority: %+v", got)
	}

	frame := signing.Frame{MessageID: h.MessageID, SenderKind: h.SenderKind, SenderID: h.SenderID,
		Text: "approve", Addresses: []string{"agent-a@receiver"}}
	if err := seal.Matches(frame); err != nil {
		t.Fatalf("intact delivered frame did not match seal: %v", err)
	}
	for name, change := range map[string]func(*signing.Frame){
		"body":       func(f *signing.Frame) { f.Text = "deny" },
		"sender":     func(f *signing.Frame) { f.SenderID = "other" },
		"message ID": func(f *signing.Frame) { f.MessageID = "other" },
		"recipient":  func(f *signing.Frame) { f.Addresses = []string{"outsider@receiver"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := frame
			change(&changed)
			if err := seal.Matches(changed); err == nil {
				t.Fatal("broker-modified frame matched original signature")
			}
		})
	}
	decoded, err := signing.ParseSeal(seal.Marshal())
	if err != nil || signing.Verify(&decoded, signedRoots(person, workspace, true, true), signedNow).State != signing.Verified {
		t.Fatalf("transported seal lost verification: %v", err)
	}
	if _, err := signing.ParseSeal(append(seal.Marshal(), 0)); err == nil {
		t.Fatal("seal with trailing bytes was accepted")
	}
}

func TestSignedIdentityAcceptanceRoomContextCannotBeChangedByBroker(t *testing.T) {
	person, device := signedKey("person-room"), signedKey("device-room")
	workspace := signing.KeyID(signedPublic(signedKey("workspace-room")))
	h := signedHeader(workspace)
	h.Recipients = []string{"agent-a@receiver", signing.RoomRecipient("operators")}
	cert := signedCert(person, device, signing.KindDevice, "chat", workspace)
	seal := signing.Sign(device, []signing.Cert{cert}, signedPublic(person), h)
	frame := signing.Frame{MessageID: h.MessageID, SenderKind: h.SenderKind, SenderID: h.SenderID,
		Text: "approve", Addresses: []string{"agent-a@receiver"}, Room: "operators"}
	if err := seal.Matches(frame); err != nil {
		t.Fatalf("intact room delivery failed verification: %v", err)
	}
	frame.Room = "another-room"
	if err := seal.Matches(frame); err == nil {
		t.Fatal("broker moved a signed message into another room")
	}
}
