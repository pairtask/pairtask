//go:build linux

package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

type attentionSummaryFrame struct {
	Type       string `json:"type"`
	Generation uint64 `json:"attention_generation"`
	Accepted   *bool  `json:"accepted"`
	Summary    *struct {
		Generation uint64   `json:"generation"`
		Count      int      `json:"count"`
		IDs        []string `json:"message_ids"`
	} `json:"attention_summary"`
}

func attentionSummaryRead(t *testing.T, p *operatorPeer) attentionSummaryFrame {
	t.Helper()
	operatorNoError(t, p.SetReadDeadline(time.Now().Add(2*time.Second)))
	line, err := p.r.ReadBytes('\n')
	operatorNoError(t, err)
	var result attentionSummaryFrame
	operatorNoError(t, json.Unmarshal(line, &result))
	return result
}

func attentionExpectSummary(t *testing.T, p *operatorPeer, ids ...string) uint64 {
	t.Helper()
	r := attentionSummaryRead(t, p)
	operatorEqual(t, "one summary frame", r.Type, "attention_summary")
	if r.Summary == nil || r.Summary.Generation == 0 {
		t.Fatal("summary needs a generation")
	}
	operatorEqual(t, "summary count", r.Summary.Count, len(ids))
	if !reflect.DeepEqual(r.Summary.IDs, ids) {
		t.Fatalf("summary IDs %v want %v", r.Summary.IDs, ids)
	}
	return r.Summary.Generation
}

func attentionSubmit(t *testing.T, p *operatorPeer, generation uint64, accepted bool) {
	t.Helper()
	attentionAgentWrite(t, p, map[string]any{"type": "attention_submitted", "attention_generation": generation})
	r := attentionSummaryRead(t, p)
	operatorEqual(t, "submission receipt result", r.Type, "attention_submitted_result")
	operatorEqual(t, "submission receipt generation", r.Generation, generation)
	if r.Accepted == nil {
		t.Fatal("receipt result omitted accepted")
	}
	operatorEqual(t, "submission receipt accepted", *r.Accepted, accepted)
}

func attentionQuiet(f *operatorFixture, receiver *operatorPeer, id string) {
	f.t.Helper()
	requireOperatorAccepted(f.t, f.send(operatorRequest(id, "worker")), "worker")
	r := attentionRead(f.t, receiver)
	operatorEqual(f.t, "quiet message ID", r.MessageID, id)
	operatorEqual(f.t, "quiet disposition", r.Attention, "suppressed")
}

func TestAttentionAcceptanceSummaryIsNotMessageAcknowledgment(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	receiver := f.peer("worker", 32)
	other := f.peer("other", 32)
	attentionSetting(f, "worker", "on")
	attentionQuiet(f, receiver, "acknowledged")
	attentionQuiet(f, receiver, "still-pending")
	receiver.write(protocol.Message{Type: "ack", MessageID: "acknowledged", Stage: "agent_acknowledged"})
	operatorEqual(t, "explicit acknowledgment persisted", attentionRead(t, receiver).Stage, "agent_acknowledged")
	attentionSetting(f, "worker", "off")
	generation := attentionExpectSummary(t, receiver, "still-pending")
	attentionSetting(f, "worker", "off") // must neither repeat nor replace the owed generation
	attentionSubmit(t, other, generation, false)
	attentionSubmit(t, receiver, generation+1, false)
	attentionSubmit(t, receiver, generation, true)
	attentionSubmit(t, receiver, generation, false)
	state := attentionSetting(f, "worker", "status")
	operatorEqual(t, "summary submission is not message acknowledgment", state.State.Pending, 1)
	operatorEqual(t, "pending message remains stored", state.State.InboxUsed, 1)
	attentionSetting(f, "worker", "off")
	requireOperatorAccepted(t, f.send(operatorRequest("after-summary", "worker")), "worker")
	r := attentionRead(t, receiver)
	operatorEqual(t, "repeated off causes no extra summary or quiet-message burst", r.MessageID, "after-summary")
	operatorEqual(t, "new ordinary delivery not suppressed", r.Attention, "")
	attentionSetting(f, "worker", "on")
	attentionQuiet(f, receiver, "next-pending")
	attentionSetting(f, "worker", "off")
	next := attentionExpectSummary(t, receiver, "still-pending", "next-pending")
	if next <= generation {
		t.Fatal("new owed summary must advance its generation")
	}
	attentionSubmit(t, receiver, generation, false)
	attentionSubmit(t, receiver, next, true)
}

func TestAttentionAcceptanceNoSummaryWhenAllAcknowledged(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	receiver := f.peer("worker", 32)
	attentionSetting(f, "worker", "on")
	attentionQuiet(f, receiver, "already-handled")
	receiver.write(protocol.Message{Type: "ack", MessageID: "already-handled", Stage: "agent_acknowledged"})
	operatorEqual(t, "explicit acknowledgment persisted", attentionRead(t, receiver).Stage, "agent_acknowledged")
	attentionSetting(f, "worker", "off")
	requireOperatorAccepted(t, f.send(operatorRequest("no-summary-barrier", "worker")), "worker")
	operatorEqual(t, "no empty summary notification", attentionRead(t, receiver).MessageID, "no-summary-barrier")
}

func TestAttentionAcceptanceRestartDefersOwedSummaryWhileOn(t *testing.T) {
	database := filepath.Join(t.TempDir(), "attention-restart.db")
	if !t.Run("persist quiet message", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		receiver := f.peer("worker", 32)
		attentionSetting(f, "worker", "on")
		attentionQuiet(f, receiver, "restart-quiet")
	}) {
		return
	}
	if !t.Run("owe then defer summary", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		state := attentionSetting(f, "worker", "status")
		operatorEqual(t, "DND survives broker restart", state.State.DND, true)
		operatorEqual(t, "quiet message survives restart", state.State.Pending, 1)
		attentionSetting(f, "worker", "off")
		attentionSetting(f, "worker", "off")
		attentionSetting(f, "worker", "on")
	}) {
		return
	}
	if !t.Run("replay remains quiet", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		receiver := f.peer("worker", 32)
		r := attentionRead(t, receiver)
		operatorEqual(t, "replayed quiet message", r.MessageID, "restart-quiet")
		operatorEqual(t, "replay marker", r.Replayed, true)
		operatorEqual(t, "persisted suppression", r.Attention, "suppressed")
		urgent := attentionCall(f, map[string]any{"type": "operator_send", "message_id": "restart-barrier", "to": "worker", "urgent": true, "payload": map[string]string{"text": "barrier"}})
		requireOperatorAccepted(t, urgent.Message, "worker")
		operatorEqual(t, "owed summary stays deferred while DND on", attentionRead(t, receiver).MessageID, "restart-barrier")
		attentionSetting(f, "worker", "off")
		generation := attentionExpectSummary(t, receiver, "restart-quiet")
		attentionSubmit(t, receiver, generation, true)
	}) {
		return
	}
	t.Run("submission receipt survives restart", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		receiver := f.peer("worker", 32)
		quiet := attentionRead(t, receiver)
		operatorEqual(t, "quiet replay keeps its decision after DND ends", quiet.Attention, "suppressed")
		operatorEqual(t, "quiet replay identity", quiet.MessageID, "restart-quiet")
		operatorEqual(t, "unacknowledged urgent also replays", attentionRead(t, receiver).MessageID, "restart-barrier")
		requireOperatorAccepted(t, f.send(operatorRequest("final-barrier", "worker")), "worker")
		operatorEqual(t, "no owed summary after saved receipt", attentionRead(t, receiver).MessageID, "final-barrier")
	})
}
