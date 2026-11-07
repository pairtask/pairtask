package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func unpairAssertEvidence(t *testing.T, f *operatorContactFixture, table string, before []string) {
	t.Helper()
	after := epochRows(t, f.raw, table, epochColumns(t, f.raw, table))
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("unpair erased or changed %s evidence", table)
	}
}

func TestContactUnpairRetirementAcceptanceQueuesEndAndGrantEvidenceRemains(t *testing.T) {
	f := pauseFenceFixture(t)
	ctx := context.Background()
	routingRegister(t, f.db, "worker")
	inbound := messageWire("undelivered-import", 4)
	inbound.From, inbound.To = "remote", []string{"worker"}
	_, err := f.db.PeerMessages().Accept(ctx, f.contact, inbound, messageAt)
	pairOK(t, err)
	beforeGrants := epochRows(t, f.raw, "peer_grants", epochColumns(t, f.raw, "peer_grants"))
	beforeReviews := epochRows(t, f.raw, "peer_review_grants", epochColumns(t, f.raw, "peer_review_grants"))
	pending, err := f.db.PendingImports(ctx, 100)
	pairOK(t, err)
	if len(pending) == 0 {
		t.Fatal("fixture must have an undelivered remote import")
	}
	_, _, err = f.db.UnpairContact(ctx, unpairRequest(t, f, "retire-queues"))
	pairOK(t, err)
	pairEqual(t, epochCount(t, f.raw, "SELECT count(*) FROM peer_messages WHERE contact=? AND direction='out' AND state='queued'", f.contact), 0)
	pairEqual(t, epochCount(t, f.raw, "SELECT count(*) FROM remote_imports WHERE contact=? AND state='pending'", f.contact), 0)
	unpairAssertEvidence(t, f, "peer_grants", beforeGrants)
	unpairAssertEvidence(t, f, "peer_review_grants", beforeReviews)
	before := unpairSnapshot(t, f)
	imported, err := f.db.ImportRemote(ctx, pending[0], 1, messageAt)
	if err != nil && !errors.Is(err, store.ErrUnpaired) {
		t.Fatal(err)
	}
	if err == nil && imported.State == store.ImportImported {
		t.Fatal("old pending import was delivered after unpair")
	}
	if !reflect.DeepEqual(before, unpairSnapshot(t, f)) {
		t.Fatal("old import attempt changed retired pairing state")
	}
	_, err = f.db.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: f.contact, ID: "after-unpair", Text: "blocked", Now: messageAt})
	pairEqual(t, errors.Is(err, store.ErrUnpaired), true)
}

func TestContactUnpairRetirementAcceptanceConsentEndsWithoutErasingEvidence(t *testing.T) {
	c := newConsentSessionCase(t)
	c.approve(t, consentMessage("session-before-unpair", 1, "X"))
	held := consentHeld(t, c.f, consentMessage("held-before-unpair", 2, "Y"), messageAt)
	f := &operatorContactFixture{db: c.f.db, raw: c.f.raw, contact: c.f.key}
	beforeSessions := epochRows(t, f.raw, "consent_sessions", epochColumns(t, f.raw, "consent_sessions"))
	beforeDecisions := epochRows(t, f.raw, "consent_decisions", epochColumns(t, f.raw, "consent_decisions"))
	if len(beforeSessions) == 0 {
		t.Fatal("fixture must have reusable session authority")
	}
	_, _, err := f.db.UnpairContact(context.Background(), unpairRequest(t, f, "retire-consent"))
	pairOK(t, err)
	pairEqual(t, epochCount(t, f.raw, "SELECT count(*) FROM consent_requests WHERE id=? AND state='withheld' AND outcome='withheld' AND outcome_reason='unpaired'", held.ID), 1)
	unpairAssertEvidence(t, f, "consent_sessions", beforeSessions)
	unpairAssertEvidence(t, f, "consent_decisions", beforeDecisions)
	_, err = f.db.DecideConsent(context.Background(), consentDecision(held, protocol.ConsentOnce, messageAt))
	if err == nil {
		t.Fatal("ended consent prompt remained decidable")
	}
	_, err = c.f.consentAccept(t, 8, consentMessage("old-session-after-unpair", 3, "X"), messageAt)
	pairEqual(t, errors.Is(err, store.ErrUnpaired), true)
}

func TestContactUnpairRetirementAcceptanceHistoryAndLocalReadsRemain(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	p := f.db.PeerMessages()
	_, err := p.Accept(ctx, f.contact, messageWire("history-in", 1), messageAt)
	pairOK(t, err)
	_, _, err = f.db.UnpairContact(ctx, unpairRequest(t, f, "keep-history"))
	pairOK(t, err)
	rows, _, err := p.Page(ctx, f.contact, 0, 100)
	pairOK(t, err)
	if len(rows) != 1 || rows[0].ID != "history-in" {
		t.Fatal("unpair made retained message history inaccessible")
	}
	marked, err := p.MarkRead(ctx, f.contact, []string{"history-in"}, messageAt)
	pairOK(t, err)
	pairEqual(t, marked, 1)
	pairEqual(t, epochCount(t, f.raw, "SELECT count(*) FROM peer_messages WHERE contact=? AND message_id='history-in' AND read_at IS NOT NULL AND receipt='local'", f.contact), 1)
	before := unpairSnapshot(t, f)
	_, _, err = p.Ack(ctx, f.contact, store.AckSelector{IDs: []string{"history-in"}}, messageAt)
	if err == nil {
		t.Fatal("unpaired history accepted a wire acknowledgment")
	}
	if !reflect.DeepEqual(before, unpairSnapshot(t, f)) {
		t.Fatal("refused history acknowledgment changed retired state")
	}
}
