//go:build linux

package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

type replyableNoticePair struct {
	asker, reviewer  *operatorPeer
	sender, receiver instantSide
	notice, outcome  protocol.Message
}

func newReplyableNoticePair(t *testing.T, reverse bool) *replyableNoticePair {
	t.Helper()
	pair := newInstantPair(t)
	sides := pair.sides()
	if reverse {
		sides[0], sides[1] = sides[1], sides[0]
	}
	f := &replyableNoticePair{sender: sides[0], receiver: sides[1]}
	f.asker, f.reviewer = f.sender.f.peer("asker", protocol.Version), f.receiver.f.peer("reviewer", protocol.Version)
	f.sender.shareRemoteAgents(t, "asker")
	f.receiver.shareRemoteAgents(t, "reviewer")
	grant := f.receiver.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpReviewGrant,
		ContactKey: f.receiver.key, ContactAgents: []string{"reviewer"}, ContactReviewGrant: &protocol.ContactReviewGrant{Mode: "auto"}, MessageID: "reply-grant"})
	operatorEqual(t, "review grant accepted", grant.Type, protocol.TypeOperatorContactResult)
	f.asker.write(protocol.Message{Type: protocol.TypeMsg, MessageID: "replyable-ask", Workspace: f.sender.key, To: "reviewer",
		ReviewAsk: &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "259"},
			Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}, Durable: true, AckRequested: true})
	operatorEqual(t, "review ask accepted", f.asker.read().Stage, protocol.StageAccepted)
	f.notice = f.reviewer.read()
	operatorEqual(t, "real notice has a review ID", f.notice.ReviewID != "", true)
	acknowledgeRemoteAgent(t, f.reviewer, f.notice.MessageID)
	f.outcome = awaitOutcomeState(t, f.asker, "requested")
	acknowledgeRemoteAgent(t, f.asker, f.outcome.MessageID)
	return f
}

func replyableChat(id, parent, workspace, to string) protocol.Message {
	return protocol.Message{Type: protocol.TypeMsg, MessageID: id, InReplyTo: parent, Workspace: workspace, To: to,
		Payload: json.RawMessage(`{"text":"ordinary chat follow-up"}`), Durable: true, AckRequested: true}
}

func replyableDelivered(t *testing.T, peer *operatorPeer, sender string) protocol.Message {
	t.Helper()
	for i := 0; i < 12; i++ {
		message := peer.read()
		if replyableOrdinaryChat(message, sender) {
			return message
		}
	}
	t.Fatal("ordinary reply was not delivered through the paired connection")
	return protocol.Message{}
}

func replyableOrdinaryChat(message protocol.Message, sender string) bool {
	if message.Type != protocol.TypeMsg {
		return false
	}
	if message.From != sender {
		return false
	}
	return message.ReviewOutcome == nil && message.ReviewID == ""
}
