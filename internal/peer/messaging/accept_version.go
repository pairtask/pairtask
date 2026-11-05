package messaging

import (
	"context"
	"time"
)

// VersionedAcceptor is a Store that answers by the session's negotiated
// version (#293): from ConsentPeerVersion on it may hold a request for its
// operator; Accept speaks the newest.
type VersionedAcceptor interface {
	AcceptVersion(ctx context.Context, contact string, a Arrival) (Answer, error)
}

// Arrival is one inbound message, the version of the session it came on
// (0 the newest) and when it arrived.
type Arrival struct {
	Version int
	Message Message
	At      time.Time
}

// acceptCall is one inbound message for a contact, at a time.
type acceptCall struct {
	contact string
	m       Message
	now     time.Time
}

// acceptAt accepts a message as a session at version does: by version
// when the store can, else as Accept.
func acceptAt(ctx context.Context, st Store, version byte, c acceptCall) (Answer, error) {
	if v, ok := st.(VersionedAcceptor); ok {
		return v.AcceptVersion(ctx, c.contact, Arrival{Version: int(version), Message: c.m, At: c.now})
	}
	return st.Accept(ctx, c.contact, c.m, c.now)
}
