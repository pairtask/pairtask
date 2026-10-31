//go:build linux

package server

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestContactNameAcceptanceOperatorRenameReturnsSnapshotAndRejectsAgent(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	key := id.Fingerprint()
	r := f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: "name", ContactKey: key, ContactText: "Office", MessageID: "rename"})
	operatorEqual(t, "rename response", r.Type, protocol.TypeOperatorContactResult)
	if r.PairingResult == nil || len(r.PairingResult.Contacts) != 1 {
		t.Fatalf("rename omitted updated contact snapshot: %+v", r)
	}
	c := r.PairingResult.Contacts[0]
	operatorEqual(t, "snapshot identity", c.IdentityKey, key)
	operatorEqual(t, "snapshot alias", c.LocalName, "Office")
	operatorEqual(t, "snapshot own configured name", r.PairingResult.SelfName, "Bob")
	before := f.count("operator_audit")
	agent := f.peer("name-agent", protocol.Version)
	agent.write(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: "name", ContactKey: key, ContactText: "Forged", MessageID: "forged-name"})
	operatorEqual(t, "agent cannot rename", agent.read().Code, protocol.ErrForbidden)
	operatorEqual(t, "forbidden request creates no audit", f.count("operator_audit"), before)
	stored, _, err := f.st.Contact(context.Background(), key)
	operatorNoError(t, err)
	operatorEqual(t, "forged name not stored", stored.LocalName, "Office")
	r = f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: "name", ContactKey: key, MessageID: "clear-name"})
	operatorEqual(t, "clear response", r.Type, protocol.TypeOperatorContactResult)
	if r.PairingResult == nil || len(r.PairingResult.Contacts) != 1 {
		t.Fatalf("clear omitted contact snapshot: %+v", r)
	}
	operatorEqual(t, "clear keeps contact", r.PairingResult.Contacts[0].IdentityKey, key)
	operatorEqual(t, "clear removes alias", r.PairingResult.Contacts[0].LocalName, "")
}
