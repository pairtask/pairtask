package chat

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

func contactSigningFixture(t *testing.T) (protocol.ContactMessageView, signingView) {
	t.Helper()
	_, person, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, device, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	const contact = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const own = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	now := time.Now().UTC().Truncate(time.Millisecond)
	personPublic := person.Public().(ed25519.PublicKey)
	cert := signing.Issue(person, signing.Cert{Kind: signing.KindDevice, Subject: "contact-chat",
		Key: device.Public().(ed25519.PublicKey), Workspace: contact,
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)})
	row := protocol.ContactMessageView{ID: "signed-contact-row", Contact: contact, Direction: "in",
		CreatedAt: now, Text: "signed content", To: []string{"our-agent"},
		ReplyID: "parent", ReplySide: protocol.ReplyByReceiver}
	header := signing.Header{MessageID: row.ID, SenderKind: signing.SenderOperator, SenderID: protocol.OperatorID,
		Workspace: contact, Recipients: (protocol.PeerLink{Receiver: own, To: row.To}).Recipients(),
		InReplyTo: "receiver:parent", CreatedAt: now, TextHash: signing.TextHash(row.Text)}
	row.Seal = protocol.EncodeSeal(signing.Sign(device, []signing.Cert{cert}, personPublic, header))
	view := signingView{own: own, roots: []protocol.SigningRoot{{KeyID: signing.KeyID(personPublic), Person: true,
		Workspace: contact, Approved: true}}}
	return row, view
}

func TestContactSigningAcceptanceChatShowsTrustAndDowngradeStates(t *testing.T) {
	row, roots := contactSigningFixture(t)
	m := &Model{}
	thread := m.contacts.thread(row.Contact)
	thread.signing = roots
	if line := m.signedRow(row); line != "· signed by their operator" {
		t.Fatalf("verified operator marker: %q", line)
	}
	thread.signing.roots[0].Approved = false
	if line := m.signedRow(row); !strings.Contains(line, "key not accepted") {
		t.Fatalf("pending person key marker: %q", line)
	}
	thread.signing.roots[0].Approved = true
	unsigned := row
	unsigned.Seal = ""
	if line := m.signedRow(unsigned); line != "· unsigned" {
		t.Fatalf("unsigned marker: %q", line)
	}
	invalid := row
	invalid.Text = "changed by relay"
	if line := m.signedRow(invalid); !strings.Contains(line, "signature doesn't hold") {
		t.Fatalf("invalid signature marker: %q", line)
	}
	downgraded := row
	downgraded.Direction, downgraded.SealDowngraded = "out", true
	if line := m.signedRow(downgraded); !strings.Contains(line, "sent unsigned") {
		t.Fatalf("v9 downgrade marker: %q", line)
	}
	collected := row
	collected.Text = ""
	if line := m.signedRow(collected); line != "" {
		t.Fatalf("collected text yielded an unverifiable marker: %q", line)
	}
}

func TestContactSigningAcceptanceChatBindsContactRecipientsAndReply(t *testing.T) {
	row, roots := contactSigningFixture(t)
	got := verifyContactRow(row, roots)
	if got.State != signing.Verified || got.Signer == nil {
		t.Fatalf("intact contact message did not verify: %+v", got)
	}
	if got.Signer.Basis != signing.BasisPerson {
		t.Fatalf("intact contact message did not verify: %+v", got)
	}
	for name, change := range map[string]func(*protocol.ContactMessageView){
		"contact": func(v *protocol.ContactMessageView) {
			v.Contact = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		},
		"recipient":       func(v *protocol.ContactMessageView) { v.To = []string{"different-agent"} },
		"reply parent":    func(v *protocol.ContactMessageView) { v.ReplyID = "different-parent" },
		"reply direction": func(v *protocol.ContactMessageView) { v.ReplySide = protocol.ReplyBySender },
		"text":            func(v *protocol.ContactMessageView) { v.Text = "changed" },
	} {
		t.Run(name, func(t *testing.T) {
			altered := row
			change(&altered)
			if got := verifyContactRow(altered, roots); got.State != signing.Invalid {
				t.Fatalf("changed signed contact field accepted: %+v", got)
			}
		})
	}
}
