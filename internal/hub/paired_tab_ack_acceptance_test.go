package hub

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func TestPairedTabAckAcceptanceOnlineReceiptAndAcknowledgmentUseResolvedSession(t *testing.T) {
	f := newPairedTabAckFixture(t)
	f.send(t, "online-paired-tag")
	f.delivery(t, "online-paired-tag")
	f.ack(t, "online-paired-tag", protocol.StageAdapterReceived)
	f.ack(t, "online-paired-tag", protocol.StageAgentAcknowledged)
	session, received, acked := f.receipt(t, "online-paired-tag")
	messageSyncEqual(t, session, f.agent.SessionID)
	messageSyncEqual(t, received, true)
	messageSyncEqual(t, acked, true)
}
func TestPairedTabAckAcceptanceWrongSessionCannotAcknowledge(t *testing.T) {
	f := newPairedTabAckFixture(t)
	f.send(t, "wrong-session-tag")
	f.delivery(t, "wrong-session-tag")
	for _, stage := range []string{protocol.StageAdapterReceived, protocol.StageAgentAcknowledged} {
		_, err := f.h.store.AcknowledgeReport(context.Background(), store.Acknowledgment{MessageID: "wrong-session-tag", Recipient: protocol.Recipient{AgentID: f.agent.ID, SessionID: "untrusted-session"}, Stage: stage, ProtocolVersion: protocol.Version})
		messageSyncEqual(t, errors.Is(err, store.ErrInvalidAck), true)
	}
	_, received, acked := f.receipt(t, "wrong-session-tag")
	messageSyncEqual(t, received, false)
	messageSyncEqual(t, acked, false)
}
func TestPairedTabAckAcceptanceOfflineRecipientBindsAtNextSession(t *testing.T) {
	f := newPairedTabAckFixture(t)
	f.h.disconnect(f.clients, f.agent)
	f.send(t, "offline-paired-tag")
	session, _, _ := f.receipt(t, "offline-paired-tag")
	messageSyncEqual(t, session, "")
	f.reconnect(t, "online-after-offline")
	f.delivery(t, "offline-paired-tag")
	f.ack(t, "offline-paired-tag", protocol.StageAdapterReceived)
	f.ack(t, "offline-paired-tag", protocol.StageAgentAcknowledged)
	session, received, acked := f.receipt(t, "offline-paired-tag")
	messageSyncEqual(t, session, f.agent.SessionID)
	messageSyncEqual(t, received, true)
	messageSyncEqual(t, acked, true)
}
func TestPairedTabAckAcceptanceReconnectRejectsOldSessionAndAcknowledgmentsReplay(t *testing.T) {
	f := newPairedTabAckFixture(t)
	f.send(t, "reconnected-paired-tag")
	f.delivery(t, "reconnected-paired-tag")
	previous := f.agent.SessionID
	f.reconnect(t, "replacement-session")
	f.delivery(t, "reconnected-paired-tag")
	_, err := f.h.store.AcknowledgeReport(context.Background(), store.Acknowledgment{MessageID: "reconnected-paired-tag", Recipient: protocol.Recipient{AgentID: f.agent.ID, SessionID: previous}, Stage: protocol.StageAdapterReceived, ProtocolVersion: protocol.Version})
	messageSyncEqual(t, errors.Is(err, store.ErrInvalidAck), true)
	for range 2 {
		f.ack(t, "reconnected-paired-tag", protocol.StageAdapterReceived)
		f.ack(t, "reconnected-paired-tag", protocol.StageAgentAcknowledged)
	}
	f.send(t, "reconnected-paired-tag")
	remoteHubEmpty(t, f.agent)
	var n int
	messageWorkerOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM durable_inbox WHERE message_id=? AND agent_id=?`, "reconnected-paired-tag", f.agent.ID).Scan(&n))
	messageSyncEqual(t, n, 1)
}
func TestPairedTabAckAcceptanceContactResultExposesPersistedHomeRouting(t *testing.T) {
	f := newPairedTabAckFixture(t)
	response := f.send(t, "persisted-route")
	f.delivery(t, "persisted-route")
	data, err := json.Marshal(response.ContactMessage)
	messageWorkerOK(t, err)
	var decoded struct {
		Here         []string `json:"here"`
		OperatorHere bool     `json:"operator_here"`
	}
	messageWorkerOK(t, json.Unmarshal(data, &decoded))
	messageSyncEqual(t, len(decoded.Here), 1)
	if len(decoded.Here) == 1 {
		messageSyncEqual(t, decoded.Here[0], f.agent.ID)
	}
	messageSyncEqual(t, decoded.OperatorHere, false)
}

func TestPairedTabAckAcceptanceHistoryReadsStoredHomeRouting(t *testing.T) {
	f := newPairedTabAckFixture(t)
	f.send(t, "history-home-routing")
	f.delivery(t, "history-home-routing")
	response := f.h.operatorContact(context.Background(), f.clients, operatorRequest{uid: 0, msg: protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: f.key}})
	messageSyncEqual(t, response.Type, protocol.TypeOperatorContactResult)
	if response.ContactPage == nil || len(response.ContactPage.Messages) != 1 {
		t.Fatalf("history page missing stored paired send: %+v", response.ContactPage)
	}
	data, err := json.Marshal(response.ContactPage.Messages[0])
	messageWorkerOK(t, err)
	var routing struct {
		Here []string `json:"here"`
	}
	messageWorkerOK(t, json.Unmarshal(data, &routing))
	messageSyncEqual(t, len(routing.Here), 1)
	if len(routing.Here) == 1 {
		messageSyncEqual(t, routing.Here[0], f.agent.ID)
	}
}
func TestPairedTabAckAcceptanceCallerCannotSupplyRecipientSession(t *testing.T) {
	f := newPairedTabAckFixture(t)
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend, MessageID: "forged-recipient-session", ContactKey: f.key, ContactText: "shared body", ContactHere: []string{f.agent.ID}, Recipients: []protocol.Recipient{{AgentID: f.agent.ID, SessionID: "caller-forged-session"}}}
	response := f.h.operatorContact(context.Background(), f.clients, operatorRequest{uid: 0, msg: msg})
	if response.Type == protocol.TypeError {
		messageSyncEqual(t, response.Code, protocol.ErrMalformedFrame)
		var n int
		messageWorkerOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM durable_inbox WHERE message_id=?`, msg.MessageID).Scan(&n))
		messageSyncEqual(t, n, 0)
		return
	}
	messageSyncEqual(t, response.Type, protocol.TypeOperatorContactResult)
	f.delivery(t, msg.MessageID)
	session, _, _ := f.receipt(t, msg.MessageID)
	messageSyncEqual(t, session, f.agent.SessionID)
}

func TestPairedTabAckAcceptanceIncomingSameIDNeverInheritsHomeMetadata(t *testing.T) {
	f := newPairedTabAckFixture(t)
	f.send(t, "same-id-both-directions")
	f.delivery(t, "same-id-both-directions")
	now := time.Now().UTC()
	answer, err := f.h.store.PeerMessages().Accept(context.Background(), f.key, messaging.Message{ID: "same-id-both-directions", N: 1, CreatedAt: now, Text: "incoming body without local tags"}, now)
	messageWorkerOK(t, err)
	messageSyncEqual(t, answer.Stored, true)
	response := f.h.operatorContact(context.Background(), f.clients, operatorRequest{uid: 0, msg: protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: f.key}})
	messageSyncEqual(t, response.Type, protocol.TypeOperatorContactResult)
	if response.ContactPage == nil {
		t.Fatal("incoming history page missing")
	}
	for _, row := range response.ContactPage.Messages {
		if row.Direction != "in" {
			continue
		}
		data, err := json.Marshal(row)
		messageWorkerOK(t, err)
		var routing struct {
			Here         []string `json:"here"`
			OperatorHere bool     `json:"operator_here"`
		}
		messageWorkerOK(t, json.Unmarshal(data, &routing))
		messageSyncEqual(t, len(routing.Here), 0)
		messageSyncEqual(t, routing.OperatorHere, false)
		return
	}
	t.Fatal("incoming same-ID row absent from history")
}

func TestPairedTabAckAcceptanceStoredHomeOperatorAndAgentNamesAreCanonical(t *testing.T) {
	f := newPairedTabAckFixture(t)
	extra := newClient("alpha-worker")
	extra.ProtocolVersion = protocol.Version
	f.h.connect(f.clients, extra)
	messageSyncEqual(t, recv(t, extra).Type, protocol.TypeWelcome)
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend, MessageID: "canonical-home-routing", ContactKey: f.key, ContactText: "ordinary text without tags", ContactHere: []string{f.agent.ID, extra.ID}, ContactOperatorHere: true}
	response := f.h.operatorContact(context.Background(), f.clients, operatorRequest{uid: 0, msg: msg})
	messageSyncEqual(t, response.Type, protocol.TypeOperatorContactResult)
	f.delivery(t, msg.MessageID)
	messageSyncEqual(t, recv(t, extra).MessageID, msg.MessageID)
	data, err := json.Marshal(response.ContactMessage)
	messageWorkerOK(t, err)
	var routing struct {
		Here         []string `json:"here"`
		OperatorHere bool     `json:"operator_here"`
	}
	messageWorkerOK(t, json.Unmarshal(data, &routing))
	messageSyncEqual(t, len(routing.Here), 2)
	if len(routing.Here) == 2 {
		messageSyncEqual(t, routing.Here[0], extra.ID)
		messageSyncEqual(t, routing.Here[1], f.agent.ID)
	}
	messageSyncEqual(t, routing.OperatorHere, true)
}
