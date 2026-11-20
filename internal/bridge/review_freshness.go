package bridge

import (
	"context"
	"time"

	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Freshness of a review request this side sent (#419): whether the change
// still is at the revisions it was requested at. The reviewing side checks
// pins when it accepts; this is the asking side looking for itself, before
// or after. It reads this agent's own forge once, within a bound, and
// changes nothing: no state, no new request, nothing sent to the other
// workspace. What it finds is told with the answer and kept nowhere.

// freshnessTimeout bounds the one forge read a check makes.
const freshnessTimeout = 5 * time.Second

// freshnessChecker looks a requested change up now.
type freshnessChecker interface {
	AskFreshness(ctx context.Context, ask protocol.ReviewOutcomeView) protocol.ReviewFreshness
}

// checkFreshness sets the freshness of each request read; a client that
// can't look says so for each.
func checkFreshness(ctx context.Context, client messagingClient, asks []protocol.ReviewOutcomeView) {
	checker, ok := client.(freshnessChecker)
	for i := range asks {
		found := unknownFreshness("this session can't reach a forge")
		if ok {
			found = checker.AskFreshness(ctx, asks[i])
		}
		asks[i].Freshness = &found
	}
}

// AskFreshness resolves the request's change on its forge and compares it
// with what was requested. Anything that keeps it from knowing is unknown
// with the reason, never current.
func (c *Client) AskFreshness(ctx context.Context, ask protocol.ReviewOutcomeView) protocol.ReviewFreshness {
	if ask.Requested == nil || ask.Forge == "" {
		return unknownFreshness("the broker didn't report what this request was pinned to; upgrade the broker")
	}
	if !bothRevisions(*ask.Requested) {
		return unknownFreshness("this request wasn't pinned to both a base and a head, so there is nothing to compare")
	}
	f, ok := c.forges[ask.Forge]
	if !ok {
		return unknownFreshness("no forge " + ask.Forge + " is set up in this session")
	}
	ctx, cancel := context.WithTimeout(ctx, freshnessTimeout)
	defer cancel()
	now, err := f.Resolve(ctx, forge.Target{Forge: ask.Forge, Repository: ask.Repository, Change: ask.Change})
	if err != nil {
		return unknownFreshness("the forge couldn't be read: " + err.Error())
	}
	if !bothRevisions(now) {
		return unknownFreshness("the forge didn't say both where the change's base and head are now")
	}
	state := protocol.FreshnessCurrent
	if now != *ask.Requested {
		state = protocol.FreshnessMoved
	}
	return protocol.ReviewFreshness{State: state, Current: &now, CheckedAt: time.Now().UTC()}
}

// bothRevisions: a base and a head, the two a comparison needs.
func bothRevisions(r protocol.Revisions) bool { return r.Base != "" && r.Head != "" }

func unknownFreshness(reason string) protocol.ReviewFreshness {
	return protocol.ReviewFreshness{State: protocol.FreshnessUnknown, CheckedAt: time.Now().UTC(), Reason: reason}
}

func (c *LazyClient) AskFreshness(ctx context.Context, ask protocol.ReviewOutcomeView) protocol.ReviewFreshness {
	client, err := c.connection(ctx)
	if err != nil {
		return unknownFreshness("no broker connection: " + err.Error())
	}
	return client.AskFreshness(ctx, ask)
}
