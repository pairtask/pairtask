package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func TestShareHereKeepsTheResolvedSessions(t *testing.T) {
	f := newOperatorContactFixture(t)
	_, err := f.db.OperatorContactSend(context.Background(), store.ContactSend{OperationID: "tagged", Contact: f.contact, Text: "question",
		Here: []string{"online", "offline"}, Recipients: []protocol.Recipient{{AgentID: "online", SessionID: "session-online"}, {AgentID: "offline"}},
		AuditID: f.audit(t, contactAuditInput{"send", "tagged"}), Now: time.Now().UTC()})
	pairOK(t, err)
	for agent, want := range map[string]string{"online": "session-online", "offline": ""} {
		var session sql.NullString
		pairOK(t, f.raw.QueryRow(`SELECT session_id FROM durable_inbox WHERE message_id = 'tagged' AND agent_id = ?`, agent).Scan(&session))
		pairEqual(t, session.String, want)
	}
}
