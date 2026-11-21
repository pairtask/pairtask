// Local attention contract agreed in peer review 25982a12 and operator 149f9bc4.
package bridge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

// These are adapter decisions after the receiving client has independently
// computed verification. Broker provenance and forged input need server tests.
func TestAttentionAcceptanceListenerRetainsWithoutInterrupting(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fields  string
		publish int
	}{
		{"ordinary", `"origin":"agent"`, 1},
		{"suppressed", `"origin":"agent","attention":"suppressed"`, 0},
		{"suppressed replay", `"origin":"agent","attention":"suppressed","replayed":true`, 0},
		{"local urgent unsigned rollout", `"origin":"operator","attention":"urgent","verification":{"state":"unsigned"}`, 1},
		{"local urgent invalid seal", `"origin":"operator","attention":"urgent","verification":{"state":"invalid"}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var msg protocol.Message
			requireNoError(t, json.Unmarshal([]byte(`{"type":"msg","message_id":"quiet-packet","from":"author","to":"worker","durable":true,"ack_requested":true,"payload":{"text":"retained content"},`+tc.fields+`}`), &msg))
			p := &testPublisher{messages: make(chan protocol.Message, 2)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			l := NewListener(ctx, nil, p)
			requireNoError(t, l.deliver([]protocol.Message{msg}))
			assertEqual(t, "host submissions", len(p.messages), tc.publish)
			assertEqual(t, "reported submissions", l.Status().Submitted, tc.publish)
			assertEqual(t, "retained fallback frames", len(l.queue), 1)
			assertEqual(t, "retained message ID", l.queue[0].message.MessageID, "quiet-packet")
			assertEqual(t, "retained payload", string(l.queue[0].message.Payload), `{"text":"retained content"}`)
			assertEqual(t, "acknowledgment remains explicit", l.queue[0].message.AckRequested, true)
		})
	}
}
