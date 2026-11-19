//go:build linux

package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

type explicitReplyPair struct {
	sides    []instantSide
	a, b     *operatorPeer
	received protocol.Message
}

func explicitReplyFixture(t *testing.T, reverse bool) explicitReplyPair {
	t.Helper()
	p := newInstantPair(t)
	sides := p.sides()
	if reverse {
		sides[0], sides[1] = sides[1], sides[0]
	}
	a, b := sides[0].f.peer("first", 13), sides[1].f.peer("second", 13)
	sides[0].shareRemoteAgents(t, "first")
	sides[1].shareRemoteAgents(t, "second")
	a.write(protocol.Message{Type: protocol.TypeMsg, Workspace: sides[0].key, To: "second", MessageID: "question",
		Payload: []byte(`{"text":"question"}`), Durable: true, AckRequested: true})
	operatorEqual(t, "question accepted", a.read().Stage, protocol.StageAccepted)
	got := b.read()
	requireWorkspaceAgent(t, got, sides[1].key, "first")
	return explicitReplyPair{sides, a, b, got}
}

func (p explicitReplyPair) reply(parent string) protocol.Message {
	return protocol.Message{Type: protocol.TypeMsg, Workspace: p.sides[1].key, To: p.received.From,
		MessageID: "explicit-answer", InReplyTo: parent, Payload: []byte(`{"text":"answer"}`), Durable: true, AckRequested: true}
}

func (p explicitReplyPair) requireAnswer(t *testing.T) {
	t.Helper()
	operatorEqual(t, "explicit reply accepted", p.b.read().Stage, protocol.StageAccepted)
	reply := p.a.read()
	requireWorkspaceAgent(t, reply, p.sides[0].key, "second")
	operatorEqual(t, "reply maps back to original local message", reply.InReplyTo, "question")
}

func TestExplicitWorkspaceReplyAcceptanceRoundTripBothDirections(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			p := explicitReplyFixture(t, reverse)
			p.b.write(p.reply(p.received.MessageID))
			p.requireAnswer(t)
		})
	}
}

func TestExplicitWorkspaceReplyAcceptanceInvalidParentCanBeCorrected(t *testing.T) {
	for _, parent := range []string{"missing-parent", "local-parent"} {
		t.Run(parent, func(t *testing.T) {
			p := explicitReplyFixture(t, false)
			local := p.sides[1].f.peer("local-only", 13)
			p.b.write(protocol.Message{Type: protocol.TypeMsg, To: "local-only", MessageID: "local-parent",
				Payload: []byte(`{"text":"local question"}`), Durable: true, AckRequested: true})
			operatorEqual(t, "local parent accepted", p.b.read().Stage, protocol.StageAccepted)
			operatorEqual(t, "local parent delivered", local.read().MessageID, "local-parent")
			p.b.write(p.reply(parent))
			refused := p.b.read()
			operatorEqual(t, "specific reply error", refused.Code, protocol.ErrInvalidReply)
			if !strings.Contains(refused.Detail, "message_id") {
				t.Fatalf("refusal must explain which ID to use: %q", refused.Detail)
			}
			// A refused send must not consume its durable ID or silently
			// turn into an unrelated new message in the other workspace.
			p.b.write(p.reply(p.received.MessageID))
			p.requireAnswer(t)
		})
	}
}
