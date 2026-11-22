package spaceowner

import (
	"bytes"
	"slices"
	"strings"
	"time"

	"github.com/pairtask/pairtask/pairbook"
)

// Outcomes of a request in a fold.
const (
	Applied          = "applied"           // in the next map
	Unchanged        = "unchanged"         // already true: the exact binding is there
	Stale            = "stale"             // made from another map: the person signs it again from the current one
	Rejected         = "rejected"          // invalid, or in conflict with an earlier request in the batch
	AwaitingApproval = "awaiting_approval" // a valid join the owner hasn't approved yet
)

// Outcome is what a fold did with one request.
type Outcome struct {
	Change pairbook.SignedChange
	Result string
}

// Batch is the requests to fold, in order, and the owner's gates: which
// joins the owner approved, and which token hashes are live invites not yet
// used.
type Batch struct {
	Changes  []pairbook.SignedChange
	Approved func(pairbook.SignedChange) bool
	Invited  func(tokenHash [32]byte) bool
}

// Fold applies a batch to c's map as one next map. Requests are checked
// against that map (their signatures and base) and against
// what earlier requests in the batch already did (in order: the first of
// two competing requests wins, the later is rejected). next is nil when
// nothing changed. Any error about cur, or about the resulting map, comes
// back as err, with no outcomes.
func (m Me) Fold(c Current, b Batch, now time.Time) (*pairbook.SignedSpaceMap, []Outcome, error) {
	v, err := m.current(c, now)
	if err != nil {
		return nil, nil, err
	}
	w := newWorking(c.Map.SpaceMap)
	out := make([]Outcome, 0, len(b.Changes))
	for _, ch := range b.Changes {
		out = append(out, Outcome{Change: ch, Result: w.fold(v, ch, b, now)})
	}
	if !w.changed {
		return nil, out, nil
	}
	next, err := m.next(w.sm, v.Mark, now)
	if err != nil {
		return nil, nil, err
	}
	return &next, out, nil
}

// invited: the owner's gate says tokenHash is a live, unused invite.
func (b Batch) invited(tokenHash [32]byte) bool {
	return b.Invited != nil && b.Invited(tokenHash)
}

// working is the next map as a batch builds it.
type working struct {
	sm      pairbook.SpaceMap
	tokens  map[[32]byte]bool
	changed bool
}

func newWorking(sm pairbook.SpaceMap) *working {
	sm.People = slices.Clone(sm.People)
	sm.Bindings = slices.Clone(sm.Bindings)
	sm.Grants = slices.Clone(sm.Grants)
	groups := make([]pairbook.Group, len(sm.Groups))
	for i, g := range sm.Groups {
		groups[i] = pairbook.Group{Name: g.Name, Members: slices.Clone(g.Members)}
	}
	sm.Groups = groups
	return &working{sm: sm, tokens: map[[32]byte]bool{}}
}

// fold is one request's outcome, applying it when it applies.
func (w *working) fold(v pairbook.VerifiedMap, c pairbook.SignedChange, b Batch, now time.Time) string {
	if c.Base != v.Mark {
		return Stale
	}
	if v.CheckChange(c, now) != nil {
		return Rejected
	}
	result := w.apply(c, b)
	if result == Applied {
		w.changed = true
	}
	return result
}

func (w *working) apply(c pairbook.SignedChange, b Batch) string {
	switch c.Action {
	case pairbook.ChangeJoin:
		return w.join(c, b)
	case pairbook.ChangeBind:
		return w.bind(c)
	case pairbook.ChangeUnbind:
		return w.unbind(c)
	case pairbook.ChangeLeave:
		return w.leave(c)
	}
	return Rejected
}

// join adds the person and their binding, once per token, if approved.
func (w *working) join(c pairbook.SignedChange, b Batch) string {
	if w.tokens[c.TokenHash] || !b.invited(c.TokenHash) {
		return Rejected
	}
	if w.hasPerson(c.Lineage) || w.bound(c.Binding.Workspace()) {
		return Rejected
	}
	if b.Approved == nil || !b.Approved(c) {
		return AwaitingApproval
	}
	w.tokens[c.TokenHash] = true
	w.sm.People = append(w.sm.People, pairbook.MapPerson{Lineage: c.Lineage, Person: c.Person, Name: c.Name, Role: pairbook.RoleMember, Evidence: c.Evidence})
	w.sm.Bindings = append(w.sm.Bindings, c.Binding)
	return Applied
}

// bind adds a workspace to a person still in the map.
func (w *working) bind(c pairbook.SignedChange) string {
	if slices.ContainsFunc(w.sm.Bindings, func(x pairbook.Binding) bool { return sameBinding(x, c.Binding) }) {
		return Unchanged
	}
	if !w.hasPerson(c.Lineage) || w.bound(c.Binding.Workspace()) {
		return Rejected
	}
	w.sm.Bindings = append(w.sm.Bindings, c.Binding)
	return Applied
}

// unbind removes the person's binding of a workspace.
func (w *working) unbind(c pairbook.SignedChange) string {
	i := slices.IndexFunc(w.sm.Bindings, func(x pairbook.Binding) bool {
		l, ok := x.ClaimedLineage()
		return ok && l == c.Lineage && x.Workspace() == c.Workspace
	})
	if i < 0 {
		return Rejected
	}
	w.sm.Bindings = slices.Delete(w.sm.Bindings, i, i+1)
	return Applied
}

// leave removes the person with everything that names them: bindings,
// group memberships and grants that select them alone.
func (w *working) leave(c pairbook.SignedChange) string {
	if !w.hasPerson(c.Lineage) {
		return Rejected
	}
	l := c.Lineage
	w.sm.People = slices.DeleteFunc(w.sm.People, func(p pairbook.MapPerson) bool { return p.Lineage == l })
	w.sm.Bindings = slices.DeleteFunc(w.sm.Bindings, func(x pairbook.Binding) bool {
		owner, ok := x.ClaimedLineage()
		return ok && owner == l
	})
	for i := range w.sm.Groups {
		w.sm.Groups[i].Members = slices.DeleteFunc(w.sm.Groups[i].Members, func(m pairbook.Lineage) bool { return m == l })
	}
	w.sm.Grants = slices.DeleteFunc(w.sm.Grants, func(g pairbook.Grant) bool { return names(g.A, l) || names(g.B, l) })
	return Applied
}

// names: s selects exactly the person l, however its hex is cased.
func names(s pairbook.Selector, l pairbook.Lineage) bool {
	return s.Kind == pairbook.SelectPerson && strings.EqualFold(s.Name, pairbook.LineageHex(l))
}

func (w *working) hasPerson(l pairbook.Lineage) bool {
	return slices.ContainsFunc(w.sm.People, func(p pairbook.MapPerson) bool { return p.Lineage == l })
}

func (w *working) bound(workspace string) bool {
	return slices.ContainsFunc(w.sm.Bindings, func(x pairbook.Binding) bool { return x.Workspace() == workspace })
}

// sameBinding: a and b are the same binding, byte for byte.
func sameBinding(a, b pairbook.Binding) bool {
	same := bytes.Equal(a.Claim, b.Claim) && bytes.Equal(a.Acceptance, b.Acceptance)
	return same && a.WorkspaceKey.Equal(b.WorkspaceKey)
}
