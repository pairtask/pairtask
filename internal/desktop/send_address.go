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
	if found := book.named(to); len(found) > 0 {
		c, err := oneWorkspace(to, found)
		return recipient{contact: c, agents: args.Agents}, err
	}
	target, err := book.agentAt(ctx, to)
	if err != nil {
		return recipient{}, err
	}
	return target, onlyAgent(target.agents[0], args.Agents)
}

// addressBook is the pairing snapshot one address is resolved against.
type addressBook struct {
	n        *Node
	contacts []protocol.PairingContactView
}

// named is every contact a workspace name or key picks out.
func (b addressBook) named(name string) []protocol.PairingContactView {
	var found []protocol.PairingContactView
	for _, c := range b.contacts {
		if slices.Contains([]string{c.LocalName, c.Name, c.IdentityKey}, name) {
			found = append(found, c)
		}
	}
	return found
}

// oneWorkspace is the single contact found for name, or the refusal that
// lists the keys of those that share it.
func oneWorkspace(name string, found []protocol.PairingContactView) (protocol.PairingContactView, error) {
	if len(found) == 1 {
		return found[0], nil
	}
	keys := make([]string, 0, len(found))
	for _, c := range found {
		keys = append(keys, c.IdentityKey)
	}
	slices.Sort(keys)
	return protocol.PairingContactView{}, errors.New(quoted(name) + " names several paired workspaces; use an identity key: " + strings.Join(keys, ", "))
}

// agentAt resolves an address that is no workspace: AGENT@WORKSPACE, or
// an agent on its own.
func (b addressBook) agentAt(ctx context.Context, to string) (recipient, error) {
	at := strings.LastIndex(to, "@")
	if at < 0 {
		return b.sharedBy(ctx, to)
	}
	agent, workspace := strings.TrimSpace(to[:at]), strings.TrimSpace(to[at+1:])
	if agent == "" || workspace == "" {
		return recipient{}, errors.New(quoted(to) + " is not AGENT@WORKSPACE: both an agent and a workspace are needed")
	}
	found := b.named(workspace)
	if len(found) == 0 {
		return recipient{}, b.unknown(workspace, "no paired workspace ")
	}
	c, err := oneWorkspace(workspace, found)
	if err != nil {
		return recipient{}, err
	}
	return recipient{contact: c, agents: []string{agent}}, b.shares(ctx, c, agent)
}

// shares refuses an agent the workspace's directory doesn't list. An
// empty directory is unknown, not a refusal: the other side decides.
func (b addressBook) shares(ctx context.Context, c protocol.PairingContactView, agent string) error {
	directory, err := b.n.directory(ctx, c.IdentityKey)
	if err != nil || len(directory) == 0 || slices.Contains(directory, agent) {
		return err
	}
	listed := slices.Clone(directory)
	slices.Sort(listed)
	return errors.New(quoted(contactName(c)) + " doesn't share an agent " + quoted(agent) + "; it shares: " + strings.Join(listed, ", "))
}

// sharedBy is the one paired workspace that says it shares agent.
func (b addressBook) sharedBy(ctx context.Context, agent string) (recipient, error) {
	var sharing []protocol.PairingContactView
	for _, c := range b.paired() {
		directory, err := b.n.directory(ctx, c.IdentityKey)
		if err != nil {
			return recipient{}, err
		}
		if slices.Contains(directory, agent) {
			sharing = append(sharing, c)
		}
	}
	switch len(sharing) {
	case 0:
		return recipient{}, b.unknown(agent, "no paired workspace or shared agent ")
	case 1:
		return recipient{contact: sharing[0], agents: []string{agent}}, nil
	}
	return recipient{}, errors.New("several paired workspaces share an agent " + quoted(agent) + "; use one of: " +
		strings.Join(qualified(agent, sharing), ", "))
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

// unknown is the refusal for a name nothing answers to, with the paired
// workspaces by display name.
func (b addressBook) unknown(name, what string) error {
	var names []string
	for _, c := range b.paired() {
		names = append(names, contactName(c))
	}
	slices.Sort(names)
	return errors.New(what + quoted(name) + "; paired workspaces: " + strings.Join(names, ", "))
}

// qualified is AGENT@NAME for each workspace, sorted; a display name
// several of them share is replaced by the identity key, so every
// suggestion names exactly one.
func qualified(agent string, contacts []protocol.PairingContactView) []string {
	uses := map[string]int{}
	for _, c := range contacts {
		uses[contactName(c)]++
	}
	out := make([]string, 0, len(contacts))
	for _, c := range contacts {
		label := contactName(c)
		if uses[label] > 1 {
			label = c.IdentityKey
		}
		out = append(out, agent+"@"+label)
	}
	slices.Sort(out)
	return out
}

// onlyAgent refuses agents given beside an address that already names
// one, unless they are exactly that agent.
func onlyAgent(agent string, agents []string) error {
	if len(agents) == 0 || slices.Equal(agents, []string{agent}) {
		return nil
	}
	return errors.New("to names the agent " + quoted(agent) + "; leave agents empty")
}

// directory is the agents the workspace with key last said it shares.
func (n *Node) directory(ctx context.Context, key string) ([]string, error) {
	out, err := n.ask(ctx, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: key},
		protocol.TypeOperatorContactResult)
	if err != nil || out.ContactPage == nil {
		return nil, errors.Join(err, missing(out.ContactPage == nil))
	}
	return out.ContactPage.Directory, nil
}
