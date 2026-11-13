package bridge

import (
	"encoding/json"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

// Verification (#115): this agent judges each message's seal itself,
// against the roots its broker welcomed it with, and the delivered frame
// against the header that was signed. The broker only carries the seal; a
// sender never sets a verification.

// verified gives a msg frame from a seal-capable broker its verification.
func (c *Client) verified(msg protocol.Message) protocol.Message {
	if msg.Type != protocol.TypeMsg || c.protocolVersion < protocol.SealVersion {
		return msg
	}
	msg.Verification = c.ownOperator(msg, verifyFrame(msg, c.roots, c.agentID, time.Now()))
	return msg
}

// takeRoots keeps what the welcome said seals verify against: the roots,
// and this workspace's key when it named it.
func (c *Client) takeRoots(welcome protocol.Message) {
	c.roots = welcome.SigningRoots
	if welcome.Identity != nil {
		c.ownWorkspace = welcome.Identity.Workspace
	}
}

// ownOperator refuses an origin=operator message verified as another
// workspace's operator: a paired workspace's person key is pinned here
// too, but only this workspace's operator speaks with operator origin.
func (c *Client) ownOperator(msg protocol.Message, v *protocol.Verification) *protocol.Verification {
	if !c.foreignOperator(msg, v) {
		return v
	}
	return &protocol.Verification{State: signing.Invalid, Reason: "signed for another workspace"}
}

// foreignOperator: an operator-origin message whose signer is another
// workspace's, when the welcome named this one.
func (c *Client) foreignOperator(msg protocol.Message, v *protocol.Verification) bool {
	if c.ownWorkspace == "" || msg.Origin != protocol.OriginOperator {
		return false
	}
	return v.Signer != nil && v.Signer.Workspace != c.ownWorkspace
}

// verifyFrame is a message's verification as recipient self sees it.
func verifyFrame(msg protocol.Message, roots []protocol.SigningRoot, self string, now time.Time) *protocol.Verification {
	if msg.Seal == "" {
		return &protocol.Verification{State: signing.Unsigned}
	}
	seal, err := protocol.DecodeSeal(msg.Seal)
	if err != nil {
		return &protocol.Verification{State: signing.Invalid, Reason: "unreadable seal"}
	}
	result := signing.Verify(&seal, protocol.RootsOf(roots), now)
	if result.State == signing.Invalid {
		return &result
	}
	if err := seal.Matches(frameOf(msg, self)); err != nil {
		return &protocol.Verification{State: signing.Invalid, Reason: err.Error()}
	}
	return &result
}

// frameOf is what the seal must have signed for msg to be what it says.
func frameOf(msg protocol.Message, self string) signing.Frame {
	kind := signing.SenderAgent
	if msg.From == protocol.OperatorID {
		kind = signing.SenderOperator
	}
	var body struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(msg.Payload, &body)
	return signing.Frame{MessageID: msg.MessageID, SenderKind: kind, SenderID: msg.From, Text: body.Text,
		Addresses: []string{self, protocol.Broadcast}, Room: msg.Room}
}
