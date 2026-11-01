package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

type pauseWireMethod struct {
	name string
	run  func(*store.PeerMessages, string) error
}

func pauseWireMethods() []pauseWireMethod {
	ctx := context.Background()
	return []pauseWireMethod{
		{"Offer", func(p *store.PeerMessages, c string) error {
			_, err := p.Offer(ctx, c, messageAt, messaging.SessionLimit)
			return err
		}},
		{"Delivered", func(p *store.PeerMessages, c string) error { return p.Delivered(ctx, c, "out", messageAt) }},
		{"Refused", func(p *store.PeerMessages, c string) error {
			return p.Refused(ctx, c, messaging.Refused{ID: "out", Refusal: messaging.Refuse(messaging.ReasonBusy)})
		}},
		{"Accept", func(p *store.PeerMessages, c string) error {
			_, err := p.Accept(ctx, c, messageWire("new-in", 4), messageAt)
			return err
		}},
		{"PendingReads", func(p *store.PeerMessages, c string) error { _, err := p.PendingReads(ctx, c, 10); return err }},
		{"ReadConfirmed", func(p *store.PeerMessages, c string) error { return p.ReadConfirmed(ctx, c, "in") }},
		{"PeerRead", func(p *store.PeerMessages, c string) error { return p.PeerRead(ctx, c, "out", messageAt) }},
		{"PendingAgentReads", func(p *store.PeerMessages, c string) error { _, err := p.PendingAgentReads(ctx, c, 10); return err }},
		{"AgentReadConfirmed", func(p *store.PeerMessages, c string) error {
			return p.AgentReadConfirmed(ctx, c, messaging.AgentRead{ID: "agent-in", Agent: "worker"})
		}},
		{"PeerAgentRead", func(p *store.PeerMessages, c string) error {
			return p.PeerAgentRead(ctx, c, messaging.AgentRead{ID: "out", Agent: "target"}, messageAt)
		}},
		{"SharedAgents", func(p *store.PeerMessages, c string) error { _, err := p.SharedAgents(ctx, c); return err }},
		{"SetPeerDirectory", func(p *store.PeerMessages, c string) error {
			return p.SetPeerDirectory(ctx, c, []string{"changed-directory"}, messageAt)
		}},
		{"PendingOutcomes", func(p *store.PeerMessages, c string) error { _, err := p.PendingOutcomes(ctx, c, 10); return err }},
		{"OutcomeAnswered", func(p *store.PeerMessages, c string) error {
			return p.OutcomeAnswered(ctx, c, messaging.OutcomeReply{AskID: "ask-in", Seq: 1})
		}},
		{"AcceptOutcome", func(p *store.PeerMessages, c string) error {
			_, err := p.AcceptOutcome(ctx, c, messaging.ReviewOutcome{AskID: "ask", Reviewer: "reviewer", Seq: 1, State: "requested"}, messageAt)
			return err
		}},
	}
}

func pauseFenceFixture(t *testing.T) *operatorContactFixture {
	t.Helper()
	f := newOperatorContactFixture(t)
	p := f.db.PeerMessages()
	ctx := context.Background()
	_, err := p.Queue(ctx, messaging.Outgoing{Contact: f.contact, ID: "out", Text: "outbound", To: []string{"target"}, Now: messageAt})
	pairOK(t, err)
	_, err = p.Accept(ctx, f.contact, messageWire("in", 1), messageAt)
	pairOK(t, err)
	_, err = p.MarkRead(ctx, f.contact, []string{"in"}, messageAt)
	pairOK(t, err)
	_, err = p.Share(ctx, f.contact, []string{"worker"}, messageAt)
	pairOK(t, err)
	reviewGrant(t, f.db, f.contact, store.ReviewAsk)
	m := remoteAsk("ask-in")
	m.N = 3
	_, err = p.Accept(ctx, f.contact, m, messageAt)
	pairOK(t, err)
	outcomeAsk(t, f.db, f.contact)
	return f
}

func pauseFenceSnapshot(t *testing.T, f *operatorContactFixture) string {
	t.Helper()
	p, ctx := f.db.PeerMessages(), context.Background()
	rows := messagePage(t, p, f.contact)
	directory, err := p.PeerDirectory(ctx, f.contact)
	pairOK(t, err)
	acks, err := p.AgentAcks(ctx, f.contact, "out")
	pairOK(t, err)
	reads, err := p.PendingReads(ctx, f.contact, 100)
	pairOK(t, err)
	agentReads, err := p.PendingAgentReads(ctx, f.contact, 100)
	pairOK(t, err)
	outcomes, err := p.PendingOutcomes(ctx, f.contact, 100)
	pairOK(t, err)
	ask, _, err := p.AskOutFor(ctx, f.contact, "ask")
	pairOK(t, err)
	encoded, err := json.Marshal([]any{rows, directory, acks, reads, agentReads, outcomes, ask})
	pairOK(t, err)
	return string(encoded)
}

type pauseFencePhase struct {
	f               *operatorContactFixture
	bound           *store.PeerMessages
	contact, before string
	active          bool
}

func newPauseFencePhase(t *testing.T, phase string) pauseFencePhase {
	t.Helper()
	f := pauseFenceFixture(t)
	p, err := f.db.PeerMessages().Admit(context.Background(), f.contact)
	pairOK(t, err)
	c := pauseFencePhase{f: f, bound: p, contact: f.contact, active: phase == "active"}
	switch phase {
	case "paused":
		pauseApply(t, f, pauseRequest(t, f, "pause", true))
	case "resumed":
		pauseApply(t, f, pauseRequest(t, f, "pause", true))
		pauseApply(t, f, pauseRequest(t, f, "resume", false))
	case "other-contact":
		c.contact = messageContact(t, f.db, 2)
	}
	c.before = pauseFenceSnapshot(t, f)
	return c
}

func (c pauseFencePhase) check(t *testing.T, method pauseWireMethod) {
	t.Helper()
	err := method.run(c.bound, c.contact)
	if c.active {
		pairOK(t, err)
		return
	}
	pairEqual(t, errors.Is(err, store.ErrFenced), true)
	pairEqual(t, pauseFenceSnapshot(t, c.f), c.before)
}

func TestContactPauseFenceAcceptanceEveryWireMethodRejectsStaleAndWrongContact(t *testing.T) {
	for _, phase := range []string{"active", "paused", "resumed", "other-contact"} {
		t.Run(phase, func(t *testing.T) {
			c := newPauseFencePhase(t, phase)
			for _, method := range pauseWireMethods() {
				t.Run(method.name, func(t *testing.T) { c.check(t, method) })
			}
		})
	}
}

func TestContactPauseFenceAcceptanceAdmissionIsRootOnlyAndResumeAdvancesGeneration(t *testing.T) {
	f := pauseFenceFixture(t)
	ctx := context.Background()
	root := f.db.PeerMessages()
	old, err := root.Admit(ctx, f.contact)
	pairOK(t, err)
	contact, g, bound := old.Fence()
	pairEqual(t, contact, f.contact)
	pairEqual(t, g, int64(0))
	pairEqual(t, bound, true)
	pauseApply(t, f, pauseRequest(t, f, "pause", true))
	_, err = root.Admit(ctx, f.contact)
	pairEqual(t, errors.Is(err, store.ErrPaused), true)
	pauseApply(t, f, pauseRequest(t, f, "resume", false))
	other := messageContact(t, f.db, 2)
	for _, key := range []string{f.contact, other} {
		_, err = old.Admit(ctx, key)
		pairEqual(t, errors.Is(err, store.ErrFenced), true)
	}
	fresh, err := root.Admit(ctx, f.contact)
	pairOK(t, err)
	_, next, _ := fresh.Fence()
	pairEqual(t, next, g+2)
	_, err = fresh.Offer(ctx, f.contact, messageAt, messaging.SessionLimit)
	pairOK(t, err)
	unconfirmed := contactReady(t, f.db, "unconfirmed-fence")
	destinationAccept(t, f.db, unconfirmed, joinAt)
	_, err = root.Admit(ctx, unconfirmed.Peer)
	pairEqual(t, errors.Is(err, messaging.ErrUnknownContact), true)
}

func TestContactPauseFenceAcceptanceBoundStoresRejectLocalOnlyMethods(t *testing.T) {
	f := pauseFenceFixture(t)
	p, err := f.db.PeerMessages().Admit(context.Background(), f.contact)
	pairOK(t, err)
	ctx := context.Background()
	before := pauseFenceSnapshot(t, f)
	other := messageContact(t, f.db, 2)
	for _, key := range []string{f.contact, other} {
		_, err = p.Admit(ctx, key)
		pairEqual(t, errors.Is(err, store.ErrFenced), true)
	}
	_, err = p.Queue(ctx, messaging.Outgoing{Contact: f.contact, ID: "local-misuse", Text: "text", Now: messageAt})
	pairEqual(t, errors.Is(err, store.ErrFenced), true)
	_, err = p.MarkRead(ctx, f.contact, []string{"in"}, messageAt)
	pairEqual(t, errors.Is(err, store.ErrFenced), true)
	_, _, err = p.Page(ctx, f.contact, 0, 10)
	pairEqual(t, errors.Is(err, store.ErrFenced), true)
	err = p.Collect(ctx, messageAt)
	pairEqual(t, errors.Is(err, store.ErrFenced), true)
	pairEqual(t, pauseFenceSnapshot(t, f), before)
}
