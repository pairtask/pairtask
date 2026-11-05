package protocol

// Paired mentions (#278): a message in a paired workspace's ⇄ conversation
// may tag this side's agents and operator too. It still goes to that
// workspace as before; the tagged here get it with the conversation named,
// and an agent's reply goes back to the same audience.

// PeerAudience is who a shared conversation's message went to on the other
// side: its operator, or some of its agents, canonical and sorted.
type PeerAudience struct {
	Operator bool     `json:"operator,omitempty"`
	Agents   []string `json:"agents,omitempty"`
}

// ConversationRef names the shared conversation a delivery belongs to:
// output only, set by the broker from what it stored.
type ConversationRef struct {
	Kind         string       `json:"kind"`    // "contact": a paired workspace's ⇄ conversation
	Contact      string       `json:"contact"` // its identity key
	Root         string       `json:"root"`    // the message replies attach to
	PeerAudience PeerAudience `json:"peer_audience"`
}

// ConversationContact is the kind of a paired workspace's conversation.
const ConversationContact = "contact"

// PeerDelivery is whether a reply in a shared conversation also went to the
// other side: queued, or not_sent and why.
type PeerDelivery struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Peer delivery states and reasons.
const (
	PeerQueued         = "queued"
	PeerNotSent        = "not_sent"
	PeerNotShared      = "not_shared"
	PeerPaused         = "paused"
	PeerContactRemoved = "contact_removed"
)
