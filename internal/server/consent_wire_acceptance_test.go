//go:build linux

package server

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"os"
	"slices"
	"testing"
	"time"
)

type consentServerFixture struct {
	*contactAckFixture
	view protocol.ConsentView
}
type consentServerVersionAccept interface {
	AcceptVersion(context.Context, string, int, messaging.Message, time.Time) (messaging.Answer, error)
}
type consentServerLegacyAccept interface {
	Accept(context.Context, string, messaging.Message, time.Time) (messaging.Answer, error)
}

func newConsentServerFixture(t *testing.T) *consentServerFixture {
	t.Helper()
	f := newContactAckFixture(t)
	_, err := f.st.ClaimPendingFor(context.Background(), protocol.Recipient{AgentID: "recipient", SessionID: "offline"}, 24)
	operatorNoError(t, err)
	response := f.call(map[string]any{"contact_op": "consent_opt", "message_id": "consent-opt-wire", "consent_on": true})
	operatorEqual(t, "consent opt-in operation supported: "+sessionJSON(response), response.text("type"), protocol.TypeOperatorContactResult)
	response = f.call(map[string]any{"contact_op": "consent_reset", "message_id": "consent-reset-wire", "consent_agent": "recipient", "consent_operation": "message"})
	operatorEqual(t, "explicit scope reset supported: "+sessionJSON(response), response.text("type"), protocol.TypeOperatorContactResult)
	now := time.Now()
	m := messaging.Message{ID: "private-wire-hold", N: 1, CreatedAt: now, From: "claimed-sender", To: []string{"recipient"}, Text: "PRIVATE PENDING WIRE BODY"}
	var answer messaging.Answer
	if s, ok := any(f.st.PeerMessages()).(consentServerVersionAccept); ok {
		answer, err = s.AcceptVersion(context.Background(), f.key, 8, m, now)
	} else {
		answer, err = any(f.st.PeerMessages()).(consentServerLegacyAccept).Accept(context.Background(), f.key, m, now)
	}
	operatorNoError(t, err)
	operatorEqual(t, "held durable request", answer.Held, true)
	response = f.call(map[string]any{"contact_op": "consent_list"})
	page := sessionDecode[protocol.ConsentPage](t, response, "consent_page")
	operatorEqual(t, "one private pending request", len(page.Items), 1)
	return &consentServerFixture{contactAckFixture: f, view: page.Items[0]}
}

func TestConsentServerAcceptancePreviewNeverReadsAcknowledgesOrAudits(t *testing.T) {
	f := newConsentServerFixture(t)
	counts := map[string]int{}
	for _, table := range []string{"operator_audit", "messages", "durable_inbox", "remote_imports", "peer_agent_reads", "peer_agent_acks"} {
		counts[table] = f.count(table)
	}
	for i := 0; i < 3; i++ {
		response := f.call(map[string]any{"contact_op": "consent_list"})
		page := sessionDecode[protocol.ConsentPage](t, response, "consent_page")
		operatorEqual(t, "immutable preview text", page.Items[0].Text, "PRIVATE PENDING WIRE BODY")
	}
	ordinary := f.call(map[string]any{"contact_op": "list"})
	page := sessionDecode[protocol.ContactMessagePage](t, ordinary, "contact_page")
	operatorEqual(t, "ordinary contact page hides pending content", len(page.Messages), 0)
	for table, before := range counts {
		operatorEqual(t, "preview preserves "+table, f.count(table), before)
	}
	reads, err := f.st.PeerMessages().PendingReads(context.Background(), f.key, 20)
	operatorNoError(t, err)
	operatorEqual(t, "no peer read receipt", len(reads), 0)
}

func TestConsentServerAcceptanceSessionGenerationIsOnAuthenticatedWire(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	owner, opened := sessionConnect(f)
	defer owner.Close()
	first := sessionDecode[protocol.SessionInfo](t, opened, "session")
	identityMissing := first.Epoch == "" || first.Gen == 0
	capabilityMissing := !slices.Contains(first.Capabilities, "consent_prompts")
	if identityMissing || capabilityMissing {
		t.Fatalf("authenticated session omitted consent identity/capability: %s", sessionJSON(opened))
	}
}
