//go:build linux

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

type instantPair struct {
	inviter, joiner            *redeemOperatorFixture
	inviterKey, joinerKey      string
	forwardDials, reverseDials atomic.Int32
	mu                         sync.Mutex
	link                       net.Conn
}

func newInstantPair(t *testing.T) *instantPair {
	t.Helper()
	return newInstantPairSeeded(t, nil)
}

func newInstantPairSeeded(t *testing.T, seed func(*instantPair)) *instantPair {
	t.Helper()
	p := &instantPair{}
	inviterCfg := redeemConfig()
	inviterCfg.MessageTick = make(chan time.Time)
	inviterCfg.Dial = func(context.Context, string) (net.Conn, error) {
		p.reverseDials.Add(1)
		return nil, errors.New("no reverse address")
	}
	inviter := newRedeemOperator(t, inviterCfg, "")
	joinerCfg := redeemConfig()
	joinerCfg.MessageTick = make(chan time.Time)
	ready := make(chan struct{})
	dial := joinerCfg.Dial
	joinerCfg.Dial = func(ctx context.Context, destination string) (net.Conn, error) {
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		p.forwardDials.Add(1)
		conn, err := dial(ctx, destination)
		if err == nil {
			p.mu.Lock()
			p.link = conn
			p.mu.Unlock()
		}
		return conn, err
	}
	joiner := newRedeemOperator(t, joinerCfg, "")
	inviterEP, err := inviter.h.PairingEndpoint()
	operatorNoError(t, err)
	joinerEP, err := joiner.h.PairingEndpoint()
	operatorNoError(t, err)
	inviterKey, joinerKey := inviterEP.Identity.Fingerprint(), joinerEP.Identity.Fingerprint()
	messageConfirm(inviter, joinerEP.Identity, 1)
	messageJoinedContact(joiner, inviterKey, relayToPairing(t, inviter))
	p.inviter, p.joiner, p.inviterKey, p.joinerKey = inviter, joiner, inviterKey, joinerKey
	if seed != nil {
		seed(p)
	}
	close(ready)
	joiner.h.SyncMessages(inviterKey)
	deadline := time.Now().Add(3 * time.Second)
	for joiner.h.MessageSyncStatus(inviterKey).LastSuccess.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("initial empty exchange did not complete")
		}
		time.Sleep(5 * time.Millisecond)
	}

	return p
}

func (p *instantPair) assertOneConnection(t *testing.T) {
	t.Helper()
	operatorEqual(t, "no reverse dial", p.reverseDials.Load(), int32(0))
	operatorEqual(t, "one forward dial", p.forwardDials.Load(), int32(1))
}

func instantSend(t *testing.T, f *redeemOperatorFixture, key, id, text string) {
	t.Helper()
	response := f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend,
		ContactKey: key, MessageID: id, ContactText: text})
	operatorEqual(t, "send accepted", response.Type, protocol.TypeOperatorContactResult)
}

// An established connection must carry a later inviter send even though
// only the joiner knows a dialable address and no periodic sync tick fires.
func TestInstantTransportAcceptanceInviterSendsAfterIdleExchange(t *testing.T) {
	p := newInstantPair(t)
	instantSend(t, p.inviter, p.joinerKey, "after-idle", "send without waiting for a poll")
	awaitMessageDelivery(t, p.inviter.st.PeerMessages(), p.joinerKey, "after-idle")
	p.assertOneConnection(t)
	response := p.joiner.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: p.inviterKey})
	if response.ContactPage == nil || len(response.ContactPage.Messages) != 1 {
		t.Fatalf("recipient must see exactly one message: %+v", response)
	}
	operatorEqual(t, "exact text", response.ContactPage.Messages[0].Text, "send without waiting for a poll")
}

func TestInstantTransportAcceptanceBothDirectionsAndReadReceipts(t *testing.T) {
	p := newInstantPair(t)
	// More than a short session's batch, to expose a link that sleeps after
	// draining only its first batch. Identical IDs on opposite sides are valid.
	const count = messaging.SessionMessages + 1
	done := make(chan struct{}, 2)
	for _, side := range []struct {
		f         *redeemOperatorFixture
		key, body string
	}{
		{p.inviter, p.joinerKey, "from inviter"}, {p.joiner, p.inviterKey, "from joiner"},
	} {
		go func() {
			defer func() { done <- struct{}{} }()
			for n := 0; n < count; n++ {
				instantSend(t, side.f, side.key, fmt.Sprintf("both-%d", n), side.body)
			}
		}()
	}
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent sends blocked")
		}
	}
	for _, side := range []struct {
		f   *redeemOperatorFixture
		key string
	}{
		{p.inviter, p.joinerKey}, {p.joiner, p.inviterKey},
	} {
		for n := 0; n < count; n++ {
			awaitMessageDelivery(t, side.f.st.PeerMessages(), side.key, fmt.Sprintf("both-%d", n))
		}
	}
	// Verify both outboxes before marking either inbox read: read receipts
	// may overtake a later delivery-state observation on a live link.
	for _, side := range []struct {
		f   *redeemOperatorFixture
		key string
	}{
		{p.inviter, p.joinerKey}, {p.joiner, p.inviterKey},
	} {
		response := side.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpRead,
			ContactKey: side.key, MessageID: "read-first", ContactRead: []string{"both-0"}})
		operatorEqual(t, "read accepted", response.Type, protocol.TypeOperatorContactResult)
		if response.ContactMarked == nil || *response.ContactMarked != 1 {
			t.Fatalf("read not marked: %+v", response)
		}
	}
	for _, side := range []struct {
		f   *redeemOperatorFixture
		key string
	}{
		{p.inviter, p.joinerKey}, {p.joiner, p.inviterKey},
	} {
		deadline := time.Now().Add(3 * time.Second)
		for {
			rows, _, err := side.f.st.PeerMessages().Page(context.Background(), side.key, 0, 64)
			operatorNoError(t, err)
			read, confirmed := false, false
			for _, row := range rows {
				if row.ID != "both-0" {
					continue
				}
				if row.Direction == messaging.DirOut {
					read = row.State == messaging.StateRead
				}
				if row.Direction == messaging.DirIn {
					confirmed = row.Receipt == messaging.ReceiptConfirmed
				}
			}
			if read && confirmed {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("read receipt not propagated: read=%v confirmed=%v", read, confirmed)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	p.assertOneConnection(t)
}

// Dropping a healthy transport cannot discard queued work or require a
// periodic sync tick to recover. The receiver sees the stable message once.
func TestInstantTransportAcceptanceReconnectWithoutPeriodicTick(t *testing.T) {
	p := newInstantPair(t)
	instantSend(t, p.inviter, p.joinerKey, "before-drop", "before disconnect")
	awaitMessageDelivery(t, p.inviter.st.PeerMessages(), p.joinerKey, "before-drop")
	p.mu.Lock()
	link := p.link
	p.mu.Unlock()
	if link == nil {
		t.Fatal("no live transport")
	}
	operatorNoError(t, link.Close())
	instantSend(t, p.inviter, p.joinerKey, "after-drop", "after disconnect")
	awaitMessageDelivery(t, p.inviter.st.PeerMessages(), p.joinerKey, "after-drop")
	response := p.joiner.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: p.inviterKey})
	if response.ContactPage == nil || len(response.ContactPage.Messages) != 2 {
		t.Fatalf("reconnect lost or duplicated rows: %+v", response)
	}
	operatorEqual(t, "inviter never dials", p.reverseDials.Load(), int32(0))
	if p.forwardDials.Load() < 2 {
		t.Fatal("disconnected transport never replaced")
	}
}

// Idle contacts must not consume the four short-session worker slots and
// prevent the fifth contact from establishing a usable live connection.
func TestInstantTransportAcceptanceFifthContactNotStarved(t *testing.T) {
	cfg := redeemConfig()
	cfg.MessageTick = make(chan time.Time)
	inviter := newRedeemOperator(t, cfg, "")
	ep, err := inviter.h.PairingEndpoint()
	operatorNoError(t, err)
	route := relayToPairing(t, inviter)
	type contact struct {
		f   *redeemOperatorFixture
		key string
	}
	contacts := make([]contact, 0, 5)
	for n := 0; n < 5; n++ {
		cfg := redeemConfig()
		cfg.MessageTick = make(chan time.Time)
		joiner := newRedeemOperator(t, cfg, "")
		joined, err := joiner.h.PairingEndpoint()
		operatorNoError(t, err)
		messageConfirm(inviter, joined.Identity, n+1)
		messageJoinedContact(joiner, ep.Identity.Fingerprint(), route)
		joiner.h.SyncMessages(ep.Identity.Fingerprint())
		deadline := time.Now().Add(3 * time.Second)
		for joiner.h.MessageSyncStatus(ep.Identity.Fingerprint()).LastSuccess.IsZero() {
			if time.Now().After(deadline) {
				t.Fatalf("contact %d starved by earlier idle contacts", n+1)
			}
			time.Sleep(5 * time.Millisecond)
		}
		contacts = append(contacts, contact{joiner, joined.Identity.Fingerprint()})
	}
	// All five connections are idle before any message is sent.
	for n, c := range contacts {
		id := fmt.Sprintf("fair-%d", n)
		instantSend(t, inviter, c.key, id, "all contacts remain reachable")
		awaitMessageDelivery(t, inviter.st.PeerMessages(), c.key, id)
		response := c.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: ep.Identity.Fingerprint()})
		if response.ContactPage == nil || len(response.ContactPage.Messages) != 1 {
			t.Fatalf("contact %d did not receive its own message: %+v", n+1, response)
		}
		operatorEqual(t, "correct contact's message", response.ContactPage.Messages[0].ID, id)
	}
}

func TestInstantTransportAcceptanceRotatesBeforeRelayByteLimit(t *testing.T) {
	for _, direction := range []string{"inviter", "joiner"} {
		t.Run(direction, func(t *testing.T) {
			p := newInstantPair(t)
			sender, recipient, key, source := p.inviter, p.joiner, p.joinerKey, p.inviterKey
			if direction == "joiner" {
				sender, recipient, key, source = p.joiner, p.inviter, p.inviterKey, p.joinerKey
			}
			text := strings.Repeat("x", messaging.TextBytes)
			const count = 30 // 480 KiB of text crosses the agreed 384 KiB rotation threshold.
			for n := 0; n < count; n++ {
				id := fmt.Sprintf("rotate-%d", n)
				instantSend(t, sender, key, id, text)
				awaitMessageDelivery(t, sender.st.PeerMessages(), key, id)
			}
			rows, _, err := recipient.st.PeerMessages().Page(context.Background(), source, 0, 64)
			operatorNoError(t, err)
			operatorEqual(t, "no lost or duplicated rotation messages", len(rows), count)
			for _, row := range rows {
				operatorEqual(t, "full payload survives rotation", row.Text, text)
			}
			if p.forwardDials.Load() < 2 {
				t.Fatal("link exceeded rotation threshold without replacing transport")
			}
			operatorEqual(t, "rotation retains dial ownership", p.reverseDials.Load(), int32(0))
		})
	}
}

func TestInstantTransportAcceptanceConfirmedPinLossStopsDelivery(t *testing.T) {
	p := newInstantPair(t)
	// There is no public revoke operation yet. Remove the confirmed contact
	// as a fault injection, then prove an already established TLS link does
	// not retain admission that the authoritative store has withdrawn.
	_, err := p.inviter.db.Exec("DELETE FROM peer_contacts WHERE identity_key = ?", p.joinerKey)
	operatorNoError(t, err)
	instantSend(t, p.joiner, p.inviterKey, "after-pin-loss", "must not arrive")
	deadline := time.Now().Add(3 * time.Second)
	for p.joiner.h.MessageSyncStatus(p.inviterKey).LastFailure == "" {
		if time.Now().After(deadline) {
			t.Fatal("link did not report loss of confirmed contact")
		}
		time.Sleep(5 * time.Millisecond)
	}
	operatorEqual(t, "no inbound message after pin loss", p.inviter.count("peer_messages"), 0)
	response := p.joiner.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: p.inviterKey})
	if response.ContactPage == nil || len(response.ContactPage.Messages) != 1 {
		t.Fatalf("sender lost queued message: %+v", response)
	}
	if response.ContactPage.Messages[0].State == messaging.StateDelivered || response.ContactPage.Messages[0].State == messaging.StateRead {
		t.Fatalf("unadmitted message reported delivered: %+v", response.ContactPage.Messages[0])
	}
}

func TestInstantTransportAcceptancePrequeuedBacklogDrainsWithoutAnotherWake(t *testing.T) {
	const count = messaging.SessionMessages + 1
	p := newInstantPairSeeded(t, func(p *instantPair) {
		for _, side := range []struct {
			f   *redeemOperatorFixture
			key string
		}{{p.inviter, p.joinerKey}, {p.joiner, p.inviterKey}} {
			for n := 0; n < count; n++ {
				_, err := side.f.st.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: side.key, ID: fmt.Sprintf("backlog-%d", n), Text: "queued before connection", Now: time.Now()})
				operatorNoError(t, err)
			}
		}
	})
	for _, side := range []struct {
		f   *redeemOperatorFixture
		key string
	}{{p.inviter, p.joinerKey}, {p.joiner, p.inviterKey}} {
		awaitMessageDelivery(t, side.f.st.PeerMessages(), side.key, fmt.Sprintf("backlog-%d", count-1))
	}
	p.assertOneConnection(t)
}
