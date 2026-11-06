package hub

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type historyHubCase struct {
	h   *Hub
	key string
	raw *sql.DB
}

func newHistoryHubCase(t *testing.T) historyHubCase {
	t.Helper()
	h, key := remoteHubFixture(t)
	raw, err := sql.Open("sqlite", strings.TrimSuffix(h.store.PeerDir(), ".peer"))
	messageWorkerOK(t, err)
	t.Cleanup(func() { raw.Close() })
	return historyHubCase{h, key, raw}
}

func (f historyHubCase) repair(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	at := time.Now().UTC()
	audit, err := f.h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "contact unpair", MessageID: "history-unpair", Scope: f.key, Result: store.AuditPending})
	messageWorkerOK(t, err)
	_, _, err = f.h.store.UnpairContact(ctx, store.ContactUnpair{OperationID: "history-unpair", Contact: f.key, Confirm: f.key, SeenEpoch: 1, AuditID: audit, At: at})
	messageWorkerOK(t, err)
	_, err = f.raw.Exec(`UPDATE peer_contacts SET pairing_epoch=2,pairing_id=?,unpaired_at='',generation=generation+1 WHERE identity_key=?`, make([]byte, 32), f.key)
	messageWorkerOK(t, err)
	_, err = f.raw.Exec(`UPDATE peer_pairing_requests SET state='accepted',delivered_at=? WHERE identity_key=?`, at.Format(time.RFC3339Nano), f.key)
	messageWorkerOK(t, err)
}

func (f historyHubCase) stage(t *testing.T, text string) {
	t.Helper()
	at := time.Now().UTC()
	result, err := f.h.store.PeerMessages().Accept(context.Background(), f.key, messaging.Message{ID: "shared-history", N: 1, Text: text, CreatedAt: at}, at)
	messageWorkerOK(t, err)
	messageSyncEqual(t, result.Stored, true)
}

func historyHubMessages(t *testing.T) historyHubCase {
	t.Helper()
	f := newHistoryHubCase(t)
	f.stage(t, "old history")
	f.repair(t)
	f.stage(t, "current history")
	return f
}

func (f historyHubCase) request(version int, op string) protocol.Message {
	return protocol.Message{Type: protocol.TypeOperatorContact, MessageID: "history-" + op, RequestID: "history-request", ProtocolVersion: version, ContactKey: f.key, ContactOp: op}
}

func TestContactHistoryHubAcceptanceLegacyListsOnlyCurrentPairing(t *testing.T) {
	for _, version := range []int{0, 23, 24} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			f := historyHubMessages(t)
			got := f.h.operatorContact(context.Background(), nil, operatorRequest{msg: f.request(version, protocol.ContactOpList)})
			messageSyncEqual(t, got.Type, protocol.TypeOperatorContactResult)
			if got.ContactPage == nil {
				t.Fatal("contact page missing")
			}
			messageSyncEqual(t, len(got.ContactPage.Messages), 1)
			messageSyncEqual(t, got.ContactPage.Messages[0].Text, "current history")
			messageSyncEqual(t, got.ContactPage.Messages[0].PairingEpoch, int64(0))
		})
	}
}

func TestContactHistoryHubAcceptanceCapableListTagsBothPairings(t *testing.T) {
	f := historyHubMessages(t)
	got := f.h.operatorContact(context.Background(), nil, operatorRequest{msg: f.request(25, protocol.ContactOpList)})
	messageSyncEqual(t, got.Type, protocol.TypeOperatorContactResult)
	if got.ContactPage == nil {
		t.Fatal("contact page missing")
	}
	messageSyncEqual(t, len(got.ContactPage.Messages), 2)
	epochs := map[int64]string{}
	for _, m := range got.ContactPage.Messages {
		epochs[m.PairingEpoch] = m.Text
	}
	messageSyncEqual(t, epochs[1], "old history")
	messageSyncEqual(t, epochs[2], "current history")
}

func TestContactHistoryHubAcceptanceLegacyExplicitReadRefusedBeforeMutation(t *testing.T) {
	f := historyHubMessages(t)
	msg := f.request(24, protocol.ContactOpRead)
	msg.ContactEpoch, msg.ContactRead = 1, []string{"shared-history"}
	got := f.h.operatorContact(context.Background(), nil, operatorRequest{msg: msg})
	messageSyncEqual(t, got.Type, protocol.TypeError)
	var changed int
	messageWorkerOK(t, f.raw.QueryRow(`SELECT count(*) FROM peer_messages WHERE read_at IS NOT NULL`).Scan(&changed))
	messageSyncEqual(t, changed, 0)
}

func TestContactHistoryHubAcceptanceExplicitReadTargetsOldPairing(t *testing.T) {
	f := historyHubMessages(t)
	msg := f.request(25, protocol.ContactOpRead)
	msg.ContactEpoch, msg.ContactRead = 1, []string{"shared-history"}
	got := f.h.operatorContact(context.Background(), nil, operatorRequest{msg: msg})
	messageSyncEqual(t, got.Type, protocol.TypeOperatorContactResult)
	var oldReceipt, currentState string
	messageWorkerOK(t, f.raw.QueryRow(`SELECT receipt FROM peer_messages WHERE pairing_epoch=1`).Scan(&oldReceipt))
	messageSyncEqual(t, oldReceipt, "local")
	messageWorkerOK(t, f.raw.QueryRow(`SELECT state FROM peer_messages WHERE pairing_epoch=2`).Scan(&currentState))
	messageSyncEqual(t, currentState, messaging.StateReceived)
	if got.ContactPage == nil {
		t.Fatal("read response lost its capable history page")
	}
	messageSyncEqual(t, len(got.ContactPage.Messages), 2)
}

func TestContactHistoryHubAcceptanceSessionNegotiatesHistory(t *testing.T) {
	for _, version := range []int{0, 23, 24, 25} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			h := newTestHub(t)
			sess := NewOperatorSession(0)
			got := h.openSession(context.Background(), operatorRequest{msg: protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: "open", ProtocolVersion: version}, sess: sess})
			messageSyncEqual(t, got.Type, protocol.TypeOperatorSession)
			messageSyncEqual(t, sess.ProtocolVersion, version)
			if got.Session == nil {
				t.Fatal("session info missing")
			}
			messageSyncEqual(t, got.Session.HasCapability(protocol.CapabilityPairingHistory), version >= 25)
		})
	}
}

func TestContactHistoryHubAcceptanceSessionCannotUpgradePerRequest(t *testing.T) {
	f := historyHubMessages(t)
	sess := NewOperatorSession(0)
	opened := f.h.openSession(context.Background(), operatorRequest{msg: protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: "open", ProtocolVersion: 24}, sess: sess})
	messageSyncEqual(t, opened.Type, protocol.TypeOperatorSession)
	msg := f.request(25, protocol.ContactOpRead)
	msg.ContactEpoch, msg.ContactRead = 1, []string{"shared-history"}
	f.h.routeSession(context.Background(), nil, operatorRequest{msg: msg, sess: sess})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, ok := sess.Next(ctx)
	messageSyncEqual(t, ok, true)
	messageSyncEqual(t, got.Type, protocol.TypeError)
	var changed int
	messageWorkerOK(t, f.raw.QueryRow(`SELECT count(*) FROM peer_messages WHERE read_at IS NOT NULL`).Scan(&changed))
	messageSyncEqual(t, changed, 0)
}

func TestContactHistoryHubAcceptanceCapableSessionKeepsNegotiatedHistory(t *testing.T) {
	f := historyHubMessages(t)
	sess := NewOperatorSession(0)
	opened := f.h.openSession(context.Background(), operatorRequest{msg: protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: "open", ProtocolVersion: 25}, sess: sess})
	messageSyncEqual(t, opened.Type, protocol.TypeOperatorSession)
	// Session requests need no repeated protocol_version header.
	msg := f.request(0, protocol.ContactOpList)
	f.h.routeSession(context.Background(), nil, operatorRequest{msg: msg, sess: sess})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, ok := sess.Next(ctx)
	messageSyncEqual(t, ok, true)
	messageSyncEqual(t, got.Type, protocol.TypeOperatorContactResult)
	if got.ContactPage == nil {
		t.Fatal("contact page missing")
	}
	messageSyncEqual(t, len(got.ContactPage.Messages), 2)
	epochs := map[int64]bool{}
	for _, m := range got.ContactPage.Messages {
		epochs[m.PairingEpoch] = true
	}
	messageSyncEqual(t, epochs[1], true)
	messageSyncEqual(t, epochs[2], true)
}
