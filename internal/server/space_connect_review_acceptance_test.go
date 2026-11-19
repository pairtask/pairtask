//go:build linux

package server

import (
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestSpaceConnectionReviewRetiredPartialAttemptCannotReturnAccepted(t *testing.T) {
	p := newConnectPair(t)
	p.b.dropNext.Store(true)
	out := p.a.connect(p.b.identity.Fingerprint())
	operatorEqual(t, "lost answer", out.Type, protocol.TypeError)
	select {
	case <-p.b.dropped:
	case <-time.After(3 * time.Second):
		t.Fatal("responder never committed partial attempt")
	}
	key := p.a.identity.Fingerprint()
	out = p.b.h.SubmitOperator(p.b.ctx, 0, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpUnpair, ContactKey: key, ContactConfirm: key, ContactEpoch: 1, MessageID: "retire-space-partial"})
	operatorEqual(t, "unpair responder", out.Type, protocol.TypeOperatorContactResult)
	retry := p.a.connect(p.b.identity.Fingerprint())
	operatorEqual(t, "retired attempt must be refused", retry.Type, protocol.TypeError)
	assertNoConnectContact(t, p.a)
	confirmed, err := p.b.st.PeerMessages().Confirmed(p.b.ctx, key)
	operatorNoError(t, err)
	operatorEqual(t, "responder stays unpaired", confirmed, false)
}
