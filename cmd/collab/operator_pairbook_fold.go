package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/spaceowner"
	"github.com/pairtask/pairtask/pairbook"
)

// The owner's fold: renew an expired map first, then fold the waiting
// requests made from it (only joins with an invite live in this owner's
// ledger; joins it doesn't know stay waiting), publish the next map, and
// drop the requests that can never apply. If another map won the publish,
// fold once more from the winner.

// pairbookApprove admits the join PERSON names and applies members'
// requests.
func pairbookApprove(ctx context.Context, s *pairbookSession, a pairbookArgs) (any, error) {
	if len(a.words) != 1 {
		return nil, errors.New("approve needs PERSON: a lineage or request prefix from requests")
	}
	return s.foldSpace(ctx, a.space, a.words[0])
}

// pairbookSync applies members' requests and renews the map when due.
func pairbookSync(ctx context.Context, s *pairbookSession, a pairbookArgs) (any, error) {
	return s.foldSpace(ctx, a.space, "")
}

// foldReport is what a fold did.
type foldReport struct {
	Version  uint64
	Applied  int
	Waiting  int
	Dropped  int
	Renewed  bool
	Changed  bool
	Approved string
}

func (r foldReport) String() string {
	if !r.Changed {
		return fmt.Sprintf("Nothing to change; %d request(s) waiting.", r.Waiting)
	}
	return fmt.Sprintf("Space map version %d: %d applied, %d waiting, %d dropped%s.", r.Version, r.Applied, r.Waiting, r.Dropped, renewedNote(r.Renewed))
}

func renewedNote(renewed bool) string {
	if renewed {
		return ", renewed"
	}
	return ""
}

func (s *pairbookSession) foldSpace(ctx context.Context, id, approve string) (any, error) {
	pin, err := s.pin(ctx, id)
	if err != nil {
		return nil, err
	}
	for try := 0; try < 2; try++ {
		rep, stale, err := s.foldOnce(ctx, pin, approve)
		if err != nil || !stale {
			return rep, err
		}
	}
	return nil, errors.New("another map kept winning; run it again")
}

// foldOnce is one fold; stale is true when another map won the publish.
func (s *pairbookSession) foldOnce(ctx context.Context, pin protocol.SpacePin, approve string) (foldReport, bool, error) {
	me, err := s.person()
	if err != nil {
		return foldReport{}, false, err
	}
	cur, renewed, stale, err := s.renewIfExpired(ctx, pin, me)
	if err != nil || stale {
		return foldReport{}, stale, err
	}
	p, err := s.pending(ctx, pin.Space)
	if err != nil {
		return foldReport{}, false, err
	}
	plan, err := planFold(p, approve)
	if err != nil {
		return foldReport{}, false, err
	}
	return s.applyFold(ctx, foldRun{pin: pin, me: me, cur: cur, plan: plan, renewed: renewed})
}

// renewIfExpired renews an expired map before anything is folded on it.
func (s *pairbookSession) renewIfExpired(ctx context.Context, pin protocol.SpacePin, me spaceowner.Me) (spaceowner.Current, bool, bool, error) {
	cur, err := s.current(ctx, pin.Space)
	if err != nil {
		return cur, false, false, err
	}
	now := time.Now()
	if now.Before(time.UnixMilli(cur.Map.ExpiresAt)) {
		return cur, false, false, nil
	}
	next, err := me.Renew(cur, now)
	if err != nil {
		return cur, false, false, err
	}
	res, err := s.publish(ctx, pin.Directory, spacePut{put: space.MapPut{Base: cur.Mark, Map: next}})
	if err != nil || res.Stale {
		return cur, false, res != nil && res.Stale, err
	}
	return spaceowner.Current{Map: next, Mark: next.SpaceMap.Mark()}, true, false, nil
}

// foldPlan is which waiting requests go into the batch, which are dropped
// at once (spent invites), which join is approved, and the live invites.
type foldPlan struct {
	batch    []pairbook.SignedChange
	spent    []pairbook.SignedChange
	waiting  int
	approved string // the approved join's digest, hex
	live     map[[32]byte]bool
}

// planFold sorts the waiting requests; an approval must name exactly one
// waiting join with a live invite.
func planFold(p pending, approve string) (foldPlan, error) {
	plan := foldPlan{live: map[[32]byte]bool{}}
	var matches []string
	for i, c := range p.changes {
		plan.place(c, p.states[i])
		if approves(c, p.states[i], approve) {
			d := c.Digest()
			matches = append(matches, hex.EncodeToString(d[:]))
		}
	}
	if approve == "" {
		return plan, nil
	}
	if len(matches) != 1 {
		return plan, fmt.Errorf("%q names %d waiting joins; give a longer prefix from requests", approve, len(matches))
	}
	plan.approved = matches[0]
	return plan, nil
}

// place puts one waiting request where it goes.
func (p *foldPlan) place(c pairbook.SignedChange, state string) {
	switch state {
	case protocol.InviteUnknown:
		p.waiting++ // not this owner's invite here: left waiting, never rejected
	case protocol.InviteSpent:
		p.spent = append(p.spent, c)
	case protocol.InviteLive:
		p.live[c.TokenHash] = true
		p.batch = append(p.batch, c)
	default:
		p.batch = append(p.batch, c)
	}
}

// approves: approve names c, a join whose invite is live.
func approves(c pairbook.SignedChange, state, approve string) bool {
	if approve == "" || state != protocol.InviteLive {
		return false
	}
	return namesJoin(c, approve)
}

// namesJoin: prefix starts the join's lineage or its request digest.
func namesJoin(c pairbook.SignedChange, prefix string) bool {
	if c.Action != pairbook.ChangeJoin {
		return false
	}
	d := c.Digest()
	return strings.HasPrefix(pairbook.LineageHex(c.Lineage), prefix) || strings.HasPrefix(hex.EncodeToString(d[:]), prefix)
}

// foldRun is one fold's inputs.
type foldRun struct {
	pin     protocol.SpacePin
	me      spaceowner.Me
	cur     spaceowner.Current
	plan    foldPlan
	renewed bool
}

// applyFold folds, renews at half-life when nothing else changed,
// publishes, and drops what can never apply.
func (s *pairbookSession) applyFold(ctx context.Context, f foldRun) (foldReport, bool, error) {
	now := time.Now()
	batch := spaceowner.Batch{Changes: f.plan.batch, Invited: func(h [32]byte) bool { return f.plan.live[h] },
		Approved: func(c pairbook.SignedChange) bool {
			d := c.Digest()
			return hex.EncodeToString(d[:]) == f.plan.approved
		}}
	next, outcomes, err := f.me.Fold(f.cur, batch, now)
	if err != nil {
		return foldReport{}, false, err
	}
	rep := reportOf(outcomes, f)
	next, err = renewWhenDue(f, next, now, &rep)
	if err != nil {
		return rep, false, err
	}
	if stale, err := s.publishNext(ctx, f, next, outcomes, &rep); err != nil || stale {
		return rep, stale, err
	}
	rep.Dropped, err = s.dropDead(ctx, f, outcomes)
	return rep, false, err
}

// renewWhenDue is next, or, when nothing else changed and half the map's
// validity has passed, the map renewed.
func renewWhenDue(f foldRun, next *pairbook.SignedSpaceMap, now time.Time, rep *foldReport) (*pairbook.SignedSpaceMap, error) {
	if next != nil || !renewDue(f.cur.Map, now) {
		return next, nil
	}
	renewed, err := f.me.Renew(f.cur, now)
	if err != nil {
		return nil, err
	}
	rep.Renewed = true
	return &renewed, nil
}

// publishNext publishes next, when there is one, into rep.
func (s *pairbookSession) publishNext(ctx context.Context, f foldRun, next *pairbook.SignedSpaceMap, outcomes []spaceowner.Outcome, rep *foldReport) (bool, error) {
	if next == nil {
		return false, nil
	}
	stale, err := s.publishFold(ctx, f, *next, outcomes)
	if err == nil && !stale {
		rep.Changed, rep.Version = true, next.Version
	}
	return stale, err
}

// deadResults are the outcomes whose request can never apply.
var deadResults = map[string]bool{spaceowner.Rejected: true, spaceowner.Stale: true, spaceowner.Unchanged: true}

// deadChanges are spent joins and requests with a dead outcome.
func deadChanges(spent []pairbook.SignedChange, outcomes []spaceowner.Outcome) []pairbook.SignedChange {
	dead := append([]pairbook.SignedChange(nil), spent...)
	for _, o := range outcomes {
		if deadResults[o.Result] {
			dead = append(dead, o.Change)
		}
	}
	return dead
}

// renewDue: half the map's validity has passed.
func renewDue(m pairbook.SignedSpaceMap, now time.Time) bool {
	half := (m.ExpiresAt - m.IssuedAt) / 2
	return pairbook.Millis(now) >= m.IssuedAt+half
}

func reportOf(outcomes []spaceowner.Outcome, f foldRun) foldReport {
	rep := foldReport{Waiting: f.plan.waiting, Renewed: f.renewed, Changed: f.renewed}
	for _, o := range outcomes {
		switch o.Result {
		case spaceowner.Applied:
			rep.Applied++
		case spaceowner.AwaitingApproval:
			rep.Waiting++
		}
	}
	return rep
}

// publishFold publishes next with the requests it applied and the
// invites their joins used.
func (s *pairbookSession) publishFold(ctx context.Context, f foldRun, next pairbook.SignedSpaceMap, outcomes []spaceowner.Outcome) (bool, error) {
	p := spacePut{put: space.MapPut{Base: f.cur.Mark, Map: next, Applied: []string{}}}
	for _, o := range outcomes {
		if o.Result != spaceowner.Applied {
			continue
		}
		d := o.Change.Digest()
		p.put.Applied = append(p.put.Applied, hex.EncodeToString(d[:]))
		if o.Change.Action == pairbook.ChangeJoin {
			p.invites = append(p.invites, map[string]string{"token_hash": hex.EncodeToString(o.Change.TokenHash[:]), "change": hex.EncodeToString(d[:])})
		}
	}
	res, err := s.publish(ctx, f.pin.Directory, p)
	return res != nil && res.Stale, err
}

// dropDead drops the requests that can never apply: rejected, stale (made
// from another map), already true, or on a spent invite. A join awaiting
// approval is kept.
func (s *pairbookSession) dropDead(ctx context.Context, f foldRun, outcomes []spaceowner.Outcome) (int, error) {
	dead := deadChanges(f.plan.spent, outcomes)
	for _, c := range dead {
		d := c.Digest()
		if _, err := s.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapDrop, Space: f.pin.Space, Digest: hex.EncodeToString(d[:])}); err != nil {
			return 0, err
		}
	}
	return len(dead), nil
}
