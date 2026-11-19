package desktop

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Addressing a send. To names where a message goes, in this order:
//
//  1. a paired workspace, by the name this side gave it, its own name or
//     its identity key; the agents are those given beside it;
//  2. AGENT@WORKSPACE, split at the last @: that workspace, and that agent;
//  3. an agent on its own, when exactly one paired workspace says it
//     shares an agent of that name.
//
// A workspace always wins, also one whose name holds an @. This is local
// addressing only: a name grants nothing, and the broker and the other
// workspace still decide what an agent may receive. A refusal lists the
// choices, so the next call can name one.

// recipient is a resolved address: the workspace, and the agents there
// (none: its operator).
type recipient struct {
	contact protocol.PairingContactView
	agents  []string
}

// target is a to that names no workspace outright, taken apart: the
// agent, and the workspace after the last @ ("" for an agent on its own).
type target struct {
	to, agent, workspace string
	qualified            bool
}

func targetOf(to string) target {
	at := strings.LastIndex(to, "@")
	if at < 0 {
		return target{to: to, agent: to}
	}
	return target{to: to, agent: strings.TrimSpace(to[:at]), workspace: strings.TrimSpace(to[at+1:]), qualified: true}
}

// address resolves args.To; nothing is sent while it is refused.
func (n *Node) address(ctx context.Context, args SendArgs) (recipient, error) {
	to := strings.TrimSpace(args.To)
	if to == "" {
		return recipient{}, errors.New("to is empty: name a paired workspace, AGENT@WORKSPACE, or an agent one workspace shares")
	}
	snap, err := n.pairing(ctx)
	if err != nil {
		return recipient{}, err
	}
	book := addressBook{n: n, contacts: snap.Contacts}
	if found := book.named(to); len(found.contacts) > 0 {
		c, err := found.one()
		return recipient{contact: c, agents: args.Agents}, err
	}
	t := targetOf(to)
	resolved, err := book.agentAt(ctx, t)
	if err != nil {
		return recipient{}, err
	}
	return resolved, t.only(args.Agents)
}

// addressBook is the pairing snapshot one address is resolved against.
type addressBook struct {
	n        *Node
	contacts []protocol.PairingContactView
}

// workspaces are the contacts a workspace name or key picked out.
type workspaces struct {
	name     string
	contacts []protocol.PairingContactView
}

// named is every contact a workspace name or key picks out.
func (b addressBook) named(name string) workspaces {
	found := workspaces{name: name}
	for _, c := range b.contacts {
		if slices.Contains([]string{c.LocalName, c.Name, c.IdentityKey}, name) {
			found.contacts = append(found.contacts, c)
		}
	}
	return found
}

// one is the single contact found, or the refusal that lists the keys of
// those that share the name.
func (w workspaces) one() (protocol.PairingContactView, error) {
	if len(w.contacts) == 1 {
		return w.contacts[0], nil
	}
	keys := make([]string, 0, len(w.contacts))
	for _, c := range w.contacts {
		keys = append(keys, c.IdentityKey)
	}
	slices.Sort(keys)
	return protocol.PairingContactView{}, errors.New(quoted(w.name) + " names several paired workspaces; use an identity key: " + strings.Join(keys, ", "))
}

// agentAt resolves a target: AGENT@WORKSPACE, or an agent on its own.
func (b addressBook) agentAt(ctx context.Context, t target) (recipient, error) {
	if !t.qualified {
		return b.sharedBy(ctx, t)
	}
	if t.agent == "" || t.workspace == "" {
		return recipient{}, errors.New(quoted(t.to) + " is not AGENT@WORKSPACE: both an agent and a workspace are needed")
	}
	found := b.named(t.workspace)
	if len(found.contacts) == 0 {
		return recipient{}, errors.New("no paired workspace " + quoted(t.workspace) + b.choices())
	}
	c, err := found.one()
	if err != nil {
		return recipient{}, err
	}
	return recipient{contact: c, agents: []string{t.agent}}, b.shares(ctx, c, t)
}

// shares refuses an agent the workspace's directory doesn't list. An
// empty directory is unknown, not a refusal: the other side decides.
func (b addressBook) shares(ctx context.Context, c protocol.PairingContactView, t target) error {
	directory, err := b.n.directory(ctx, c)
	if err != nil || len(directory) == 0 {
		return err
	}
	if slices.Contains(directory, t.agent) {
		return nil
	}
	listed := slices.Clone(directory)
	slices.Sort(listed)
	return errors.New(quoted(contactName(c)) + " doesn't share an agent " + quoted(t.agent) + "; it shares: " + strings.Join(listed, ", "))
}

// sharedBy is the one paired workspace that says it shares the agent.
func (b addressBook) sharedBy(ctx context.Context, t target) (recipient, error) {
	var sharing []protocol.PairingContactView
	for _, c := range b.paired() {
		directory, err := b.n.directory(ctx, c)
		if err != nil {
			return recipient{}, err
		}
		if slices.Contains(directory, t.agent) {
			sharing = append(sharing, c)
		}
	}
	switch len(sharing) {
	case 0:
		return recipient{}, errors.New("no paired workspace or shared agent " + quoted(t.agent) + b.choices())
	case 1:
		return recipient{contact: sharing[0], agents: []string{t.agent}}, nil
	}
	return recipient{}, errors.New("several paired workspaces share an agent " + quoted(t.agent) + "; use one of: " +
		strings.Join(b.suggestions(t, sharing), ", "))
}

// paired are the contacts that carry messages: confirmed and not unpaired.
func (b addressBook) paired() []protocol.PairingContactView {
	var out []protocol.PairingContactView
	for _, c := range b.contacts {
		if c.Confirmed && c.UnpairedAt == nil {
			out = append(out, c)
		}
	}
	return out
}

// choices ends a refusal for a name nothing answers to: the paired
// workspaces by display name.
func (b addressBook) choices() string {
	var names []string
	for _, c := range b.paired() {
		names = append(names, contactName(c))
	}
	slices.Sort(names)
	return "; paired workspaces: " + strings.Join(names, ", ")
}

// suggestions is AGENT@NAME for each of the workspaces, sorted. Every one
// can be passed back as to and reach exactly that workspace: a display
// name that wouldn't (see label) is replaced by the identity key.
func (b addressBook) suggestions(t target, contacts []protocol.PairingContactView) []string {
	out := make([]string, 0, len(contacts))
	for _, c := range contacts {
		out = append(out, t.agent+"@"+b.label(t, c))
	}
	slices.Sort(out)
	return out
}

// label is c's display name when AGENT@name resolves back to c alone, else
// its identity key. A name doesn't when it holds an @ (the address is
// split at the last one), when any other contact answers to it too, or
// when the whole address happens to be a workspace's name.
func (b addressBook) label(t target, c protocol.PairingContactView) string {
	name := contactName(c)
	if strings.Contains(name, "@") || len(b.named(name).contacts) != 1 {
		return c.IdentityKey
	}
	if len(b.named(t.agent+"@"+name).contacts) > 0 {
		return c.IdentityKey
	}
	return name
}

// only refuses agents given beside an address that already names one,
// unless they are exactly that agent.
func (t target) only(agents []string) error {
	if len(agents) == 0 || slices.Equal(agents, []string{t.agent}) {
		return nil
	}
	return errors.New("to names the agent " + quoted(t.agent) + "; leave agents empty")
}

// directory is the agents the workspace last said it shares.
func (n *Node) directory(ctx context.Context, c protocol.PairingContactView) ([]string, error) {
	out, err := n.ask(ctx, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: c.IdentityKey},
		protocol.TypeOperatorContactResult)
	if err != nil || out.ContactPage == nil {
		return nil, errors.Join(err, missing(out.ContactPage == nil))
	}
	return out.ContactPage.Directory, nil
}
