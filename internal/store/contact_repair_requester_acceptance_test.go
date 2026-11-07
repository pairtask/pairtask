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

func TestContactRepairRequesterAcceptanceNewHoldAndOutcomeUseOnlyCurrentEpoch(t *testing.T) {
	f := namespaceFixture(t)
	wireQueue(t, f, "same-held", "old body")
	pairOK(t, f.db.PeerMessages().HeldBy(context.Background(), f.key, messaging.Held{ID: "same-held", HeldID: "old-hold"}, messageAt))
	namespaceRepair(t, f)
	oldHolds := namespaceEpochRows(t, f, "consent_held_out", 1)
	oldMessages := namespaceEpochRows(t, f, "peer_messages", 1)
	wireQueue(t, f, "same-held", "new body")
	bound := wireBound(t, f)
	pairOK(t, bound.HeldBy(context.Background(), f.key, messaging.Held{ID: "same-held", HeldID: "new-hold"}, messageAt))
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM consent_held_out WHERE pairing_epoch=2 AND held_id='new-hold' AND state='held'`), 1)
	pairEqual(t, wireCurrentState(t, f, "same-held"), messaging.StateDelivered)
	outcome := messaging.ConsentOutcome{ID: "same-held", State: "declined", Reason: "operator_declined"}
	pairOK(t, bound.AcceptConsentOutcome(context.Background(), f.key, outcome, messageAt))
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM consent_held_out WHERE pairing_epoch=2 AND state='declined'`), 1)
	if !reflect.DeepEqual(oldHolds, namespaceEpochRows(t, f, "consent_held_out", 1)) {
		t.Error("new hold or outcome changed retired hold evidence")
	}
	if !reflect.DeepEqual(oldMessages, namespaceEpochRows(t, f, "peer_messages", 1)) {
		t.Error("new held delivery changed retired outbox history")
	}
}

func TestContactRepairRequesterAcceptanceHeldForOldOnlyIDIsUnknown(t *testing.T) {
	f := namespaceFixture(t)
	wireQueue(t, f, "old-only-held", "old body")
	namespaceRepair(t, f)
	before := wireMutationSnapshot(t, f)
	err := wireBound(t, f).HeldBy(context.Background(), f.key, messaging.Held{ID: "old-only-held", HeldID: "late-old-hold"}, messageAt)
	if !errors.Is(err, messaging.ErrProtocol) {
		t.Errorf("new session held an unknown current ID: %v", err)
	}
	if !reflect.DeepEqual(before, wireMutationSnapshot(t, f)) {
		t.Fatal("held frame for retired-only ID changed authority or history")
	}
}

func TestContactRepairRequesterAcceptanceOutcomeRefusalLeavesRetiredConsentUntouched(t *testing.T) {
	old := newConsentPublishCase(t, protocol.ConsentOnce, true)
	old.decide(t)
	old.dispatch(t)
	namespaceRepair(t, old.f)
	prior := namespaceEpochRows(t, old.f, "consent_requests", 1)
	pairEqual(t, epochCount(t, old.f.raw, `SELECT count(*) FROM consent_requests WHERE pairing_epoch=1 AND outcome='pending'`), 1)
	fresh := &consentPublishCase{f: old.f, m: old.m, kind: protocol.ConsentOnce, view: consentHeld(t, old.f, old.m, messageAt)}
	fresh.decide(t)
	imp := namespacePending(t, old.f, old.m.ID)
	result, err := old.f.db.ImportRemote(context.Background(), imp, 2, messageAt)
	pairOK(t, err)
	pairEqual(t, result.State, store.ImportImported)
	pairEqual(t, epochCount(t, old.f.raw, `SELECT count(*) FROM consent_requests WHERE pairing_epoch=2 AND outcome='pending'`), 1)
	pairOK(t, wireBound(t, old.f).ConsentOutcomeRefused(context.Background(), old.f.key, messaging.ConsentOutcomeRefused{ID: old.m.ID, Reason: "invalid"}))
	pairEqual(t, epochCount(t, old.f.raw, `SELECT count(*) FROM consent_requests WHERE pairing_epoch=2 AND outcome='refused'`), 1)
	if !reflect.DeepEqual(prior, namespaceEpochRows(t, old.f, "consent_requests", 1)) {
		t.Fatal("current consent refusal rewrote retired allowed-consent outcome")
	}
}

func TestContactRepairRequesterAcceptanceActiveHoldReplayRemainsTerminal(t *testing.T) {
	f := namespaceFixture(t)
	wireQueue(t, f, "active-held", "active body")
	bound := wireBound(t, f)
	held := messaging.Held{ID: "active-held", HeldID: "active-hold"}
	pairOK(t, bound.HeldBy(context.Background(), f.key, held, messageAt))
	pairOK(t, bound.HeldBy(context.Background(), f.key, held, messageAt))
	allowed := messaging.ConsentOutcome{ID: "active-held", State: "allowed"}
	pairOK(t, bound.AcceptConsentOutcome(context.Background(), f.key, allowed, messageAt))
	before := wireMutationSnapshot(t, f)
	pairOK(t, bound.AcceptConsentOutcome(context.Background(), f.key, allowed, messageAt))
	err := bound.AcceptConsentOutcome(context.Background(), f.key, messaging.ConsentOutcome{ID: "active-held", State: "declined"}, messageAt)
	pairEqual(t, errors.Is(err, messaging.ErrConsentTerminalConflict), true)
	if !reflect.DeepEqual(before, wireMutationSnapshot(t, f)) {
		t.Fatal("replayed or contradictory outcome changed terminal hold")
	}
}
