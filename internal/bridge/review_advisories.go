package bridge

import (
	"context"
	"time"

	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Review advisories (#304): before an agent's review request is queued,
// its pins are checked against the change, for at most pinPreflight; the
// broker's advice comes back on the request's accepted stage. Advice only:
// the request is sent as given whatever either says.

// pinPreflight bounds the pin check an agent's request waits for.
const pinPreflight = 5 * time.Second

// acceptedWait bounds how long a request waits for its accepted stage, to
// return the broker's advice; the stage stays on the inbox either way.
const acceptedWait = 2 * time.Second

// checkPins is what a check of the request's pins found, nil when all is
// as pinned.
func (c *Client) checkPins(ctx context.Context, ask *protocol.ReviewAsk) []protocol.ReviewAdvisory {
	if ask == nil || ask.Base == "" || ask.Head == "" {
		return nil
	}
	f, ok := c.forges[ask.Forge]
	if !ok {
		return []protocol.ReviewAdvisory{{Code: protocol.AdvisoryNotChecked, Class: forge.ClassUnsupported}}
	}
	ctx, cancel := context.WithTimeout(ctx, pinPreflight)
	defer cancel()
	return pinAdvisories(forge.CheckPins(ctx, f, ask.ReviewTarget, protocol.Revisions{Base: ask.Base, Head: ask.Head}))
}

func pinAdvisories(checks []forge.PinCheck) []protocol.ReviewAdvisory {
	var out []protocol.ReviewAdvisory
	for _, c := range checks {
		out = append(out, protocol.ReviewAdvisory{Code: c.Code, Current: c.Current, Class: c.Class})
	}
	return out
}

// watchAccepted hands the message's accepted stage to the caller as well
// as to the inbox.
func (c *Client) watchAccepted(id string) chan protocol.Message {
	ch := make(chan protocol.Message, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accepted == nil {
		c.accepted = map[string]chan protocol.Message{}
	}
	c.accepted[id] = ch
	return ch
}

func (c *Client) unwatchAccepted(id string) {
	c.mu.Lock()
	delete(c.accepted, id)
	c.mu.Unlock()
}

// seeAccepted copies an accepted stage to whoever watches for it; the
// frame goes on to the inbox regardless.
func (c *Client) seeAccepted(msg protocol.Message) {
	if msg.Type != protocol.TypeAck || msg.Stage != protocol.StageAccepted {
		return
	}
	c.mu.Lock()
	ch := c.accepted[msg.MessageID]
	delete(c.accepted, msg.MessageID)
	c.mu.Unlock()
	if ch != nil {
		select {
		case ch <- msg:
		default:
		}
	}
}

// brokerAdvice is the advice on the request's accepted stage, if it comes
// in time.
func brokerAdvice(ctx context.Context, accepted <-chan protocol.Message) []protocol.ReviewAdvisory {
	select {
	case ack := <-accepted:
		return ack.ReviewAdvisories
	case <-time.After(acceptedWait):
	case <-ctx.Done():
	}
	return nil
}
