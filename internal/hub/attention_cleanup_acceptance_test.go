package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func attentionMarkTable(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT s.name FROM sqlite_schema s WHERE s.type='table'
AND EXISTS (SELECT 1 FROM pragma_table_info(s.name) WHERE name='message_id')
AND EXISTS (SELECT 1 FROM pragma_table_info(s.name) WHERE name='agent_id')
AND EXISTS (SELECT 1 FROM pragma_table_info(s.name) WHERE name='suppressed_at')`)
	messageWorkerOK(t, err)
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		messageWorkerOK(t, rows.Scan(&name))
		names = append(names, name)
	}
	messageWorkerOK(t, rows.Err())
	if len(names) != 1 {
		t.Fatalf("expected one persisted attention mark table, got %v", names)
	}
	return `"` + strings.ReplaceAll(names[0], `"`, `""`) + `"`
}

type attentionStoredRows struct {
	db    *sql.DB
	table string
}

func (rows attentionStoredRows) count(t *testing.T, message, agent string) int {
	t.Helper()
	var count int
	messageWorkerOK(t, rows.db.QueryRow("SELECT count(*) FROM "+rows.table+" WHERE message_id=? AND agent_id=?", message, agent).Scan(&count))
	return count
}

func TestAttentionAcceptanceCollectionFollowsExactParent(t *testing.T) {
	h, removedContact := remoteHubFixture(t)
	keptContact := remoteHubContact(t, h, 2)
	ctx := context.Background()
	receiver := remoteHubClient(t, h, "quiet", 32)
	clients := map[string]*Client{receiver.ID: receiver}
	var request protocol.Message
	messageWorkerOK(t, json.Unmarshal([]byte(`{"type":"operator_attention","agent_id":"quiet","attention_op":"on"}`), &request))
	response := h.routeOperator(ctx, clients, operatorRequest{ctx: ctx, msg: request})
	messageSyncEqual(t, response.Type, "operator_attention_result")
	parents := map[string]string{}
	for _, contact := range []string{removedContact, keptContact} {
		remoteHubShare(t, h, contact, receiver.ID)
		remoteHubStage(t, h, contact, messaging.Message{ID: "same-remote-id", N: 1, From: "remote-agent", To: []string{receiver.ID}})
		h.drainImports(ctx, clients)
		parents[contact] = recv(t, receiver).MessageID
	}
	if parents[removedContact] == parents[keptContact] {
		t.Fatal("fixture needs distinct local message identities")
	}
	raw, err := sql.Open("sqlite", strings.TrimSuffix(h.store.PeerDir(), ".peer"))
	messageWorkerOK(t, err)
	defer raw.Close()
	marks := attentionStoredRows{raw, attentionMarkTable(t, raw)}
	inbox := attentionStoredRows{raw, "durable_inbox"}
	for _, id := range parents {
		messageSyncEqual(t, marks.count(t, id, receiver.ID), 1)
	}
	_, err = h.store.PeerMessages().Unshare(ctx, removedContact, []string{receiver.ID}, time.Now())
	messageWorkerOK(t, err)
	messageSyncEqual(t, inbox.count(t, parents[removedContact], receiver.ID), 0)
	messageWorkerOK(t, h.store.PeerMessages().Collect(ctx, time.Now().Add(25*time.Hour)))
	messageSyncEqual(t, marks.count(t, parents[removedContact], receiver.ID), 0)
	messageSyncEqual(t, inbox.count(t, parents[keptContact], receiver.ID), 1)
	messageSyncEqual(t, marks.count(t, parents[keptContact], receiver.ID), 1)
}
