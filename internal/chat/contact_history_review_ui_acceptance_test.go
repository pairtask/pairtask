package chat

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Capture requests emitted by actual model commands, without a network.
type historyReviewWire struct{ sent []protocol.Message }

func (w *historyReviewWire) Request(m protocol.Message) (string, error) {
	w.sent = append(w.sent, m)
	return m.RequestID, nil
}
func (w *historyReviewWire) Incoming() <-chan protocol.Message { return nil }
func (w *historyReviewWire) Close() error                      { return nil }

func historyReviewModel(t *testing.T) (*Model, *historyReviewWire) {
	t.Helper()
	m := New(Options{})
	t.Cleanup(m.cancel)
	w := &historyReviewWire{}
	m.transport, m.baseline = w, true
	m.session.Capabilities = protocol.SessionCapabilitiesFor(25)
	return m, w
}

func historyReviewRun(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, child := range batch {
			historyReviewRun(child)
		}
	}
}

func historyReviewAsked(epoch int64) protocol.ReviewAskedItem {
	return protocol.ReviewAskedItem{Contact: "sha256:review-history", AskID: "same-ask", PairingEpoch: epoch, Reviewer: "reviewer", State: "completed", Settled: true}
}

func historyReviewPage(m *Model, items ...protocol.ReviewAskedItem) {
	m.reviewPendingResult(pendingRequest{ids: []string{protocol.ReviewOpAsked, "first", "0"}}, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewAsked: &protocol.ReviewAskedPage{Items: items, Total: len(items)}})
}

func TestContactHistoryReviewUIAcceptanceAskedRefsKeepEpochIdentityAcrossRefresh(t *testing.T) {
	m, _ := historyReviewModel(t)
	old, current := historyReviewAsked(1), historyReviewAsked(2)
	historyReviewPage(m, old, current)
	if len(m.sections.asked.items) != 2 {
		t.Fatalf("asked rows=%d, want both pairings", len(m.sections.asked.items))
	}
	oldRef := m.sections.ref(askedKey(old))
	currentRef := m.sections.ref(askedKey(current))
	if oldRef == currentRef {
		t.Fatal("reused ask ID got one selectable ref across pairings")
	}
	historyReviewPage(m, current, old)
	if m.sections.ref(askedKey(old)) != oldRef {
		t.Fatal("refresh changed the old pairing's ref")
	}
	if m.sections.ref(askedKey(current)) != currentRef {
		t.Fatal("refresh changed the current pairing's ref")
	}
}

func TestContactHistoryReviewUIAcceptanceFindingsRequestCarriesListedEpoch(t *testing.T) {
	m, w := historyReviewModel(t)
	old := historyReviewAsked(1)
	historyReviewPage(m, old)
	ref := m.sections.ref(askedKey(old))
	historyReviewRun(m.askedFindings(ref))
	if len(w.sent) != 1 {
		t.Fatalf("findings requests=%d, want1", len(w.sent))
	}
	req := w.sent[0]
	if req.ReviewOp != protocol.ReviewOpAskedDetail {
		t.Fatalf("requestop=%q", req.ReviewOp)
	}
	if len(req.ReviewAskRefs) != 1 {
		t.Fatal("findings request did not select one ask")
	}
	want := protocol.ReviewAskRef{Contact: old.Contact, AskID: old.AskID, PairingEpoch: 1}
	if req.ReviewAskRefs[0] != want {
		t.Fatalf("findings ref=%+v, want %+v", req.ReviewAskRefs[0], want)
	}
}

func TestContactHistoryReviewUIAcceptanceFailedRefsKeepBothPairings(t *testing.T) {
	m, _ := historyReviewModel(t)
	old := protocol.ReviewFailedItem{Kind: protocol.FailedAsk, Contact: "sha256:review-history", ID: "same-ask", PairingEpoch: 1, State: "refused"}
	current := old
	current.PairingEpoch = 2
	m.failedPage(&protocol.ReviewFailedPage{Items: []protocol.ReviewFailedItem{old, current}, Total: 2, Token: "both-epochs"}, true)
	if len(m.sections.failed.items) != 2 {
		t.Fatalf("failed rows=%d, want both pairings", len(m.sections.failed.items))
	}
	if m.sections.ref(failedKey(old)) == m.sections.ref(failedKey(current)) {
		t.Fatal("failed pairings share one selectable ref")
	}
}

func historyReviewQueuedCard(t *testing.T) (*Model, *historyReviewWire, *actionCard) {
	t.Helper()
	m, w := historyReviewModel(t)
	c := &actionCard{id: "queued-card", kind: cardRemoteReview, contact: "sha256:review-history", review: &reviewProgress{}}
	if !m.startCard(c) {
		t.Fatal("card fixture rejected")
	}
	m.remoteReviewResult(pendingRequest{ids: []string{c.contact, "same-ask", "queued", c.id}}, protocol.Message{Type: protocol.TypeOperatorContactResult, ContactMessage: &protocol.ContactMessageView{Contact: c.contact, ID: "same-ask", PairingEpoch: 1, Direction: "out", State: "queued"}})
	return m, w, c
}

func TestContactHistoryReviewUIAcceptanceCardRefreshKeepsQueueEpoch(t *testing.T) {
	m, w, c := historyReviewQueuedCard(t)
	historyReviewRun(m.readCardAsks())
	if len(w.sent) != 1 {
		t.Fatalf("card refreshrequests=%d, want1", len(w.sent))
	}
	refs := w.sent[0].ReviewAskRefs
	if len(refs) != 1 {
		t.Fatal("card refresh did not select one ask")
	}
	want := protocol.ReviewAskRef{Contact: c.contact, AskID: "same-ask", PairingEpoch: 1}
	if refs[0] != want {
		t.Fatalf("card ref=%+v, want %+v", refs[0], want)
	}
}

func (w *historyReviewWire) answerCard(t *testing.T, m *Model, item protocol.ReviewAskedItem) {
	t.Helper()
	historyReviewRun(m.readCardAsks())
	if len(w.sent) != 1 {
		t.Fatal("card refresh missing")
	}
	m.cardAsksResult(m.pending[w.sent[0].RequestID], protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{item}, Total: 1}})
}

func TestContactHistoryReviewUIAcceptanceCardCannotAdoptAnotherEpochOutcome(t *testing.T) {
	m, w, c := historyReviewQueuedCard(t)
	w.answerCard(t, m, historyReviewAsked(2))
	if c.review.item != nil && c.review.item.PairingEpoch == 2 {
		t.Fatal("old queue card adopted a reused ask's outcome from another pairing")
	}
}

func TestContactHistoryReviewUIAcceptanceCardAcceptsMatchingEpochOutcome(t *testing.T) {
	m, w, c := historyReviewQueuedCard(t)
	w.answerCard(t, m, historyReviewAsked(1))
	if c.review.item == nil {
		t.Fatal("matching source epoch outcome was lost")
	}
	if c.review.item.PairingEpoch != 1 {
		t.Fatalf("card source epoch=%d,want1", c.review.item.PairingEpoch)
	}
	if !finishedCard(c) {
		t.Fatal("matching completed outcome did not finish its card")
	}
}

func TestContactHistoryReviewUIAcceptanceLegacyRefKeepsCurrentEpochDefault(t *testing.T) {
	m, w := historyReviewModel(t)
	old := historyReviewAsked(0)
	historyReviewPage(m, old)
	historyReviewRun(m.askedFindings(m.sections.ref(askedKey(old))))
	if len(w.sent) != 1 {
		t.Fatal("legacy findings request missing")
	}
	want := protocol.ReviewAskRef{Contact: old.Contact, AskID: old.AskID}
	if len(w.sent[0].ReviewAskRefs) != 1 {
		t.Fatal("legacy findings request did not select one ask")
	}
	if w.sent[0].ReviewAskRefs[0] != want {
		t.Fatalf("legacy ref changed: %+v", w.sent[0].ReviewAskRefs[0])
	}
}
