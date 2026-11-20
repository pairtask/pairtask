package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

type attentionRecordingConn struct {
	net.Conn
	written bytes.Buffer
}

func (c *attentionRecordingConn) Write(data []byte) (int, error)   { return c.written.Write(data) }
func (c *attentionRecordingConn) SetWriteDeadline(time.Time) error { return nil }

func attentionSubmissionListener(t *testing.T, failure error) (*Listener, *testPublisher, *attentionRecordingConn) {
	t.Helper()
	connection := &attentionRecordingConn{}
	client := &Client{conn: connection, writeGate: make(chan struct{}, 1), done: make(chan struct{}), protocolVersion: 32}
	lazy := &LazyClient{gate: make(chan struct{}, 1), client: client}
	publisher := &testPublisher{messages: make(chan protocol.Message, 4), err: failure}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return NewListener(ctx, lazy, publisher), publisher, connection
}

func attentionSubmissionFrame(t *testing.T, wire string) protocol.Message {
	t.Helper()
	var message protocol.Message
	requireNoError(t, json.Unmarshal([]byte(wire), &message))
	return message
}

func TestAttentionAcceptanceListenerReportsOnlySuccessfulSummarySubmission(t *testing.T) {
	for _, fails := range []bool{false, true} {
		name := "submitted"
		var failure error
		if fails {
			name = "host failed"
			failure = errors.New("host unavailable")
		}
		t.Run(name, func(t *testing.T) {
			listener, publisher, connection := attentionSubmissionListener(t, failure)
			message := attentionSubmissionFrame(t, `{"type":"attention_summary","attention_summary":{"generation":73,"count":1,"message_ids":["quiet"]}}`)
			err := listener.deliver([]protocol.Message{message})
			assertEqual(t, "summary retained for fallback", len(listener.queue), 1)
			if fails {
				if err == nil {
					t.Fatal("host failure hidden")
				}
				assertEqual(t, "failed host submission sends no receipt", connection.written.Len(), 0)
				assertEqual(t, "failed host submission not reported", listener.Status().Submitted, 0)
				return
			}
			requireNoError(t, err)
			assertEqual(t, "one summary submitted", len(publisher.messages), 1)
			attentionCheckSubmissionReceipt(t, connection)
		})
	}
}

func TestAttentionAcceptanceSubmissionResultDoesNotInterruptHost(t *testing.T) {
	listener, publisher, connection := attentionSubmissionListener(t, nil)
	message := attentionSubmissionFrame(t, `{"type":"attention_submitted_result","attention_generation":73,"accepted":true}`)
	requireNoError(t, listener.deliver([]protocol.Message{message}))
	assertEqual(t, "control result is not another host interruption", len(publisher.messages), 0)
	assertEqual(t, "control result does not trigger another receipt", connection.written.Len(), 0)
}

func attentionCheckSubmissionReceipt(t *testing.T, connection *attentionRecordingConn) {
	t.Helper()
	var receipt map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(connection.written.Bytes()), &receipt); err != nil {
		t.Fatalf("one summary receipt required: %q (%v)", connection.written.String(), err)
	}
	assertEqual(t, "receipt type", receipt["type"], "attention_submitted")
	generation, ok := receipt["attention_generation"].(float64)
	assertEqual(t, "receipt generation is numeric", ok, true)
	assertEqual(t, "receipt names submitted generation", generation, float64(73))
	for _, field := range []string{"agent_id", "message_id", "stage"} {
		if receipt[field] != nil {
			t.Fatalf("summary receipt must not contain %s: %v", field, receipt)
		}
	}
}
