package desktop

import (
	"context"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Fresh inboxes: this node is the only side that dials, so between its own
// sessions (every 30 s while active, 5 min idle) nothing new reaches it, not
// even the agents a workspace just shared. check_inbox asks each workspace
// for a session first, then reads each conversation once that session
// completed a round or failed, within one budget for them all: what it shows
// is what they hold now, or it says it isn't.

const (
	syncBudget = 5 * time.Second
	syncPoll   = 100 * time.Millisecond
)

// pendingSync is one requested session: when it was asked for, the failures
// counted then, and why none could be asked for.
type pendingSync struct {
	at       time.Time
	failures int
	refused  string
}

// freshInbox requests a session with every contact, then reads each
// conversation as its session ends or the budget runs out.
func (n *Node) freshInbox(ctx context.Context, contacts []protocol.PairingContactView, limit int) ([]Conversation, error) {
	syncs := make([]pendingSync, len(contacts))
	for i, c := range contacts {
		syncs[i] = n.requestSync(ctx, c.IdentityKey)
	}
	deadline := time.Now().Add(syncBudget)
	out := make([]Conversation, 0, len(contacts))
	for i, c := range contacts {
		conv, err := n.awaitConversation(ctx, c, limit, syncs[i], deadline)
		if err != nil {
			return nil, err
		}
		out = append(out, conv)
	}
	return out, nil
}

// requestSync asks for a session with contact; a refusal (a paused or
// unpaired contact) is kept as the reason, not an error.
func (n *Node) requestSync(ctx context.Context, contact string) pendingSync {
	p := pendingSync{at: time.Now()}
	out, err := n.ask(ctx, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSync, ContactKey: contact},
		protocol.TypeOperatorContactResult)
	switch {
	case err != nil:
		p.refused = err.Error()
	case out.ContactSync != nil:
		p.failures = out.ContactSync.Failures
	}
	return p
}

// awaitConversation reads the conversation with c until its session ended
// or the deadline passed, and says which.
func (n *Node) awaitConversation(ctx context.Context, c protocol.PairingContactView, limit int, p pendingSync, deadline time.Time) (Conversation, error) {
	for {
		conv, sync, err := n.conversation(ctx, c, limit)
		if err != nil {
			return conv, err
		}
		done, fresh := p.outcome(sync)
		if done || p.refused != "" || !time.Now().Before(deadline) {
			conv.Fresh, conv.SyncFailure = fresh, p.failure(sync, fresh)
			return conv, nil
		}
		if err := sleep(ctx, syncPoll); err != nil {
			return conv, err
		}
	}
}

// outcome reports whether the session asked for has ended, and whether it
// reached them: a round completed since the request (a live link's round
// counts), or a failure counted since.
func (p pendingSync) outcome(s protocol.ContactSyncView) (done, fresh bool) {
	if s.LastSuccess != nil && !s.LastSuccess.Before(p.at) {
		return true, true
	}
	return s.Failures > p.failures, false
}

// failure is why a conversation isn't fresh, or "".
func (p pendingSync) failure(s protocol.ContactSyncView, fresh bool) string {
	switch {
	case fresh:
		return ""
	case p.refused != "":
		return p.refused
	case s.Failures > p.failures && s.LastFailure != "":
		return s.LastFailure
	}
	return "no session with them finished within " + syncBudget.String()
}

// sleep waits d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
