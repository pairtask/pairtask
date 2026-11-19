//go:build linux

package server

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestAttentionAcceptanceNonDurableQuietHasHonestAdvisoryWithoutStoredSummary(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	receiver := f.peer("quiet", 32)
	sender := f.peer("sender", 32)
	attentionSetting(f, "quiet", "on")
	sender.write(protocol.Message{Type: "msg", MessageID: "ephemeral-quiet", To: "quiet", Durable: false, AckRequested: true, Payload: json.RawMessage(`{"text":"explicitly non-durable"}`)})
	accepted := attentionRead(t, sender)
	requireOperatorAccepted(t, accepted.Message, "quiet")
	operatorEqual(t, "non-durable guarantee preserved", accepted.Durable, false)
	delivered := attentionRead(t, receiver)
	operatorEqual(t, "non-durable message reaches adapter", delivered.MessageID, "ephemeral-quiet")
	operatorEqual(t, "non-durable host interruption suppressed", delivered.Attention, "suppressed")
	state := attentionSetting(f, "quiet", "status")
	operatorEqual(t, "non-durable is not durable summary work", state.State.Pending, 0)
	operatorEqual(t, "non-durable does not consume durable inbox", state.State.InboxUsed, 0)
	attentionSetting(f, "quiet", "off")
	requireOperatorAccepted(t, f.send(operatorRequest("ephemeral-barrier", "quiet")), "quiet")
	operatorEqual(t, "no stored summary for ephemeral message", attentionRead(t, receiver).MessageID, "ephemeral-barrier")
	operatorEqual(t, "sender told actual suppression", attentionAdvisories(t, accepted.Message)["quiet"], "suppressed")
}
