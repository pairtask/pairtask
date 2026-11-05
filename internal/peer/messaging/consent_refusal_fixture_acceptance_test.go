package messaging_test

import (
	"context"
	"database/sql"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
	"strings"
	"testing"
)

type consentRefusalStore interface {
	PendingConsentOutcomes(context.Context, string, int) ([]messaging.ConsentOutcome, error)
	ConsentOutcomeRefused(context.Context, string, messaging.ConsentOutcomeRefused) error
}
type consentConflictPair struct {
	left, right         *store.Store
	leftPath, rightPath string
	request, hold       string
}

func consentRefusalQueue(t *testing.T, db *store.Store) consentRefusalStore {
	t.Helper()
	q, ok := any(db.PeerMessages()).(consentRefusalStore)
	if !ok {
		t.Fatal("consent outcome refusal persistence is not implemented")
	}
	return q
}
func newConsentConflictPair(t *testing.T) consentConflictPair {
	t.Helper()
	left, lp := sessionFixture(t)
	right, rp := sessionFixture(t)
	sessionSuccess(t, sessionExchange(t, left.PeerMessages(), right.PeerMessages(), 0))
	pair := consentConflictPair{left: left, right: right, leftPath: lp, rightPath: rp, request: "contradictory-request"}
	pair.hold = consentRefusalPending(t, right, pair.request, 1)
	_, err := left.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: sessionContact, ID: pair.request, Text: "held immutable body", To: []string{"recipient"}, Now: sessionAt})
	sessionOK(t, err)
	cs := consentOutcomeDurableStore(t, left.PeerMessages())
	sessionOK(t, cs.HeldBy(context.Background(), sessionContact, messaging.Held{ID: pair.request, HeldID: pair.hold}, sessionAt))
	sessionOK(t, cs.AcceptConsentOutcome(context.Background(), sessionContact, messaging.ConsentOutcome{ID: pair.request, State: "allowed"}, sessionAt))
	return pair
}
func consentRefusalPending(t *testing.T, db *store.Store, id string, n uint64) string {
	t.Helper()
	ctx := context.Background()
	_, err := db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "recipient", SessionID: "offline"}, 24)
	sessionOK(t, err)
	sessionOK(t, db.SetConsentOptIn(ctx, store.ConsentOptIn{Contact: sessionContact, On: true, AuditID: 0, Now: sessionAt}))
	sessionOK(t, db.ResetConsentScope(ctx, store.ConsentReset{Contact: sessionContact, Agent: "recipient", Operation: "message", AuditID: 0, Now: sessionAt}))
	a, err := db.PeerMessages().AcceptVersion(ctx, sessionContact, messaging.Arrival{Version: 8, Message: messaging.Message{ID: id, N: n, CreatedAt: sessionAt, From: "", To: []string{"recipient"}, Text: "held immutable body"}, At: sessionAt})
	sessionOK(t, err)
	if !a.Held {
		t.Fatalf("request wasn't held: %+v", a)
	}
	page, err := db.ConsentPage(ctx, sessionContact, 0, 20)
	sessionOK(t, err)
	for _, v := range page.Items {
		if v.ID == a.HeldID {
			_, err = db.DecideConsent(ctx, store.ConsentDecision{ID: v.ID, Digest: v.Digest, Revision: v.Revision, Choice: protocol.ConsentDecline, Now: sessionAt})
			sessionOK(t, err)
			return v.ID
		}
	}
	t.Fatal("held request was not previewable")
	return ""
}
func consentRefusalOtherContact(t *testing.T, db *store.Store) string {
	t.Helper()
	key := "sha256:" + strings.Repeat("b", 64)
	ctx := context.Background()
	_, err := db.Commit(ctx, peer.PairingOutcome{OperationID: "other-operation", InvitationID: "other-invitation", IdentityKey: key, Name: "Other", TranscriptID: make([]byte, 64), At: sessionAt})
	sessionOK(t, err)
	_, err = db.Accept(ctx, key)
	sessionOK(t, err)
	_, err = db.Deliver(ctx, key, peer.PairingAccepted, sessionAt)
	sessionOK(t, err)
	return key
}
func consentRefusalDiagnosis(t *testing.T, path, id string) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	sessionOK(t, err)
	defer raw.Close()
	rows, err := raw.Query(`SELECT * FROM consent_requests WHERE contact=? AND message_id=?`, sessionContact, id)
	sessionOK(t, err)
	defer rows.Close()
	columns, err := rows.Columns()
	sessionOK(t, err)
	if !rows.Next() {
		t.Fatal("refused outcome was collected instead of diagnosed")
	}
	values := consentScanDiagnosis(t, rows, columns)
	refused, reason := consentRefusalColumns(columns, values)
	if !refused || !reason {
		t.Fatalf("refusal and reason not persisted: columns=%v values=%v", columns, values)
	}
}

func consentScanDiagnosis(t *testing.T, rows *sql.Rows, columns []string) []any {
	t.Helper()
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	sessionOK(t, rows.Scan(pointers...))
	return values
}

func consentRefusalColumns(columns []string, values []any) (bool, bool) {
	refused, reason := false, false
	for i, name := range columns {
		if name == "outcome" && values[i] == "refused" {
			refused = true
		}
		if strings.Contains(name, "reason") && values[i] == "terminal_conflict" {
			reason = true
		}
	}
	return refused, reason
}
