//go:build linux

package server

import (
	"bytes"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestSpaceConnectionRecoveryEndedAttemptAllowsFreshExplicitConnect(t *testing.T) {
	p := newConnectPair(t)
	p.b.dropNext.Store(true)
	out := p.a.connect(p.b.identity.Fingerprint())
	operatorEqual(t, "lost answer", out.Type, protocol.TypeError)
	select {
	case <-p.b.dropped:
	case <-time.After(3 * time.Second):
		t.Fatal("responder never committed partial attempt")
	}
	_, oldID := p.b.pairing(t, p.a.identity.Fingerprint())
	key := p.a.identity.Fingerprint()
	out = p.b.h.SubmitOperator(p.b.ctx, 0, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpUnpair, ContactKey: key, ContactConfirm: key, ContactEpoch: 1, MessageID: "end-space-attempt"})
	operatorEqual(t, "unpair", out.Type, protocol.TypeOperatorContactResult)
	retry := p.a.connect(p.b.identity.Fingerprint())
	operatorEqual(t, "ended reply fails this call", retry.Type, protocol.TypeError)
	assertNoConnectContact(t, p.a)
	var pending int
	operatorNoError(t, p.a.db.QueryRow(`SELECT count(*) FROM space_attempts WHERE space=? AND target=?`, connectSpaceID, p.b.identity.Fingerprint()).Scan(&pending))
	operatorEqual(t, "definitively ended attempt removed", pending, 0)
	requireConnected(t, p.a.connect(p.b.identity.Fingerprint()))
	aEpoch, aID := p.a.pairing(t, p.b.identity.Fingerprint())
	bEpoch, bID := p.b.pairing(t, p.a.identity.Fingerprint())
	operatorEqual(t, "dialer first installed epoch", aEpoch, int64(1))
	operatorEqual(t, "responder fresh epoch", bEpoch, int64(2))
	operatorEqual(t, "same new pairing ID", bytes.Equal(aID, bID), true)
	operatorEqual(t, "retired pairing ID not reused", bytes.Equal(aID, oldID), false)
	assertConnectNoGrants(t, p.a)
	assertConnectNoGrants(t, p.b)
}
