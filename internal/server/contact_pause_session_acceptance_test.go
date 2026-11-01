//go:build linux

package server

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func pauseServerContact(t *testing.T, f *redeemOperatorFixture, key string, paused bool) {
	t.Helper()
	op := "resume"
	if paused {
		op = "pause"
	}
	audit, err := f.st.RecordOperatorAudit(context.Background(), store.OperatorAudit{Action: "contact " + op, MessageID: op, Scope: key, Result: store.AuditPending})
	operatorNoError(t, err)
	_, err = f.st.SetContactPaused(context.Background(), store.ContactPause{OperationID: op, Contact: key, Paused: paused, AuditID: audit, At: time.Now()})
	operatorNoError(t, err)
}

func pauseServerRemoteStore(t *testing.T, key string) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "remote.db"))
	operatorNoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx, at := context.Background(), time.Now()
	_, err = db.Commit(ctx, peer.PairingOutcome{OperationID: "remote-pair", InvitationID: "remote-invitation", IdentityKey: key, Name: "Peer", TranscriptID: make([]byte, 64), At: at})
	operatorNoError(t, err)
	_, err = db.Accept(ctx, key)
	operatorNoError(t, err)
	_, err = db.Deliver(ctx, key, peer.PairingAccepted, at)
	operatorNoError(t, err)
	return db
}

func TestContactPauseServerAcceptancePausedTLSAndAdmissionRefuseNewSessions(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	ep, err := f.h.PairingEndpoint()
	operatorNoError(t, err)
	route := messageRoute{relayToPairing(t, f), ep.Identity.Fingerprint()}
	pauseServerContact(t, f, id.Fingerprint(), true)
	_, err = ep.Admit(context.Background(), id.Fingerprint())
	operatorEqual(t, "paused admission", errors.Is(err, store.ErrPaused), true)
	if conn, err := messageConnect(t, route, id); err == nil {
		conn.Close()
		t.Fatal("paused contact admitted a TLS message session")
	}
}

func TestContactPauseServerAcceptanceInboundSessionFenceSurvivesLiveHandoff(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	ep, err := f.h.PairingEndpoint()
	operatorNoError(t, err)
	destination := relayToPairing(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", destination)
	operatorNoError(t, err)
	defer raw.Close()
	conn, err := firstcontact.MessageClient(ctx, raw, id, time.Now, ep.Identity.Fingerprint())
	operatorNoError(t, err)
	defer conn.Close()
	remote := pauseServerRemoteStore(t, ep.Identity.Fingerprint())
	s := messaging.Session{Frames: conn.MessageFrames(ctx), Store: remote.PeerMessages(), Contact: ep.Identity.Fingerprint(), Now: time.Now}
	_, version, err := s.OpenDial(ctx)
	operatorNoError(t, err)
	s.Version = version
	if version < messaging.LiveVersion {
		t.Fatal("fixture did not negotiate live sessions")
	}
	on, err := s.OfferLive(true)
	operatorNoError(t, err)
	operatorEqual(t, "live handoff", on, true)
	// Bypass dropLive deliberately: transaction fencing must be sufficient.
	pauseServerContact(t, f, id.Fingerprint(), true)
	pauseServerContact(t, f, id.Fingerprint(), false)
	_, err = remote.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: ep.Identity.Fingerprint(), ID: "after-pause", Text: "must stay queued", Now: time.Now()})
	operatorNoError(t, err)
	wake := make(chan struct{}, 1)
	wake <- struct{}{}
	link := messaging.Link{Session: s, Open: func(ctx context.Context) messaging.Frames { return conn.MessageFrames(ctx) }, Wake: wake,
		Timing: messaging.LinkTiming{Round: time.Second, Grace: time.Second, Heartbeat: time.Second, Rotate: 1 << 20, MaxAge: time.Minute}}
	if err = link.RunDialer(ctx); err == nil {
		t.Fatal("stale inbound live session continued after pause/resume")
	}
	rows, _, err := f.st.PeerMessages().Page(context.Background(), id.Fingerprint(), 0, 100)
	operatorNoError(t, err)
	operatorEqual(t, "nothing stored through stale session", len(rows), 0)
	rows, _, err = remote.PeerMessages().Page(context.Background(), ep.Identity.Fingerprint(), 0, 100)
	operatorNoError(t, err)
	operatorEqual(t, "retry remains queued", rows[0].State, messaging.StateQueued)
}

func TestContactPauseServerAcceptanceOnlyOperatorCanMutateAndSnapshotTracksPause(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	key := id.Fingerprint()
	before := f.count("operator_audit")
	agent := f.peer("untrusted-agent", protocol.Version)
	agent.write(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: "pause", ContactKey: key, MessageID: "forged-pause"})
	operatorEqual(t, "agent cannot pause", agent.read().Code, protocol.ErrForbidden)
	operatorEqual(t, "forbidden mutation has no audit", f.count("operator_audit"), before)
	c, _, err := f.st.Contact(context.Background(), key)
	operatorNoError(t, err)
	operatorEqual(t, "agent cannot change pause state", c.PausedAt.IsZero(), true)
	for _, op := range []string{"pause", "resume"} {
		out := f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: op, ContactKey: key, MessageID: "operator-" + op})
		operatorEqual(t, "operator mutation accepted", out.Type, protocol.TypeOperatorContactResult)
		if out.PairingResult == nil || len(out.PairingResult.Contacts) != 1 {
			t.Fatal("pause omitted current contact snapshot")
		}
		operatorEqual(t, "snapshot pause state", out.PairingResult.Contacts[0].PausedAt != nil, op == "pause")
	}
}
