package messaging_test

import (
	"context"
	"encoding/json"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
	"testing"
)

func TestConsentReviewLifecycleAcceptanceSessionEndSettlesBothWithoutDuplicateRequest(t *testing.T) {
	left, _ := sessionFixture(t)
	right, _ := sessionFixture(t)
	ctx := context.Background()
	_, err := right.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "reviewer", SessionID: "offline"}, 24)
	sessionOK(t, err)
	sessionOK(t, right.SetConsentOptIn(ctx, sessionContact, true, 0, sessionAt))
	for _, op := range []string{"review", "publish"} {
		sessionOK(t, right.ResetConsentScope(ctx, sessionContact, "reviewer", op, 0, sessionAt))
	}
	id := "one-correlated-review"
	_, err = left.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: sessionContact, ID: id, Text: "held exact review", To: []string{"reviewer"}, Review: &messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "293", Base: "base", Head: "head", Publish: true}, Now: sessionAt})
	sessionOK(t, err)
	sessionSuccess(t, sessionExchange(t, left.PeerMessages(), right.PeerMessages(), 0))
	page, err := right.ConsentPage(ctx, sessionContact, 0, 20)
	sessionOK(t, err)
	sessionEqual(t, len(page.Items), 1)
	v := page.Items[0]
	_, err = right.DecideConsent(ctx, store.ConsentDecision{ID: v.ID, Digest: v.Digest, Revision: v.Revision, Choice: "session", Evidence: store.ConsentEvidence{Kind: "session", Epoch: "ended-broker", Gen: 7}, Now: sessionAt})
	sessionOK(t, err)
	imports, err := right.PendingImports(ctx, 20)
	sessionOK(t, err)
	sessionEqual(t, len(imports), 1)
	_, err = right.WithholdConsent(ctx, imports[0], "session_ended", sessionAt)
	sessionOK(t, err)
	reviewOutcomes, err := right.PeerMessages().PendingOutcomes(ctx, sessionContact, 20)
	sessionOK(t, err)
	sessionEqual(t, len(reviewOutcomes), 1)
	sessionEqual(t, reviewOutcomes[0].AskID, id)
	sessionEqual(t, reviewOutcomes[0].State, "withheld")
	consentOutcomes, err := consentRefusalQueue(t, right).PendingConsentOutcomes(ctx, sessionContact, 20)
	sessionOK(t, err)
	sessionEqual(t, len(consentOutcomes), 1)
	sessionEqual(t, consentOutcomes[0].ID, id)
	sessionEqual(t, consentOutcomes[0].Reason, "session_ended")
	sessionSuccess(t, sessionExchange(t, left.PeerMessages(), right.PeerMessages(), 0))
	asked, err := left.AskedReviews(ctx, "")
	sessionOK(t, err)
	sessionEqual(t, asked.Total, 1)
	sessionEqual(t, len(asked.Items), 1)
	item := asked.Items[0]
	sessionEqual(t, item.AskID, id)
	sessionEqual(t, item.State, "withheld")
	sessionEqual(t, item.Settled, true)
	data, err := json.Marshal(item)
	sessionOK(t, err)
	var annotations struct {
		Consent string `json:"consent"`
		Reason  string `json:"consent_reason"`
	}
	sessionOK(t, json.Unmarshal(data, &annotations))
	sessionEqual(t, annotations.Consent, "withheld")
	sessionEqual(t, annotations.Reason, "session_ended")
	reviewOutcomes, err = right.PeerMessages().PendingOutcomes(ctx, sessionContact, 20)
	sessionOK(t, err)
	sessionEqual(t, len(reviewOutcomes), 0)
	consentOutcomes, err = consentRefusalQueue(t, right).PendingConsentOutcomes(ctx, sessionContact, 20)
	sessionOK(t, err)
	sessionEqual(t, len(consentOutcomes), 0)
}
