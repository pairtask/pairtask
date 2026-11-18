package desktop

import (
	"context"
	"errors"

	"github.com/pairtask/pairtask/internal/protocol"
)

// SpaceJoinArgs are space_join's arguments.
type SpaceJoinArgs struct {
	Link string `json:"link" jsonschema:"The invite link the space's owner shared: https://HOST/v0/spaces/ID#token=...&owner=..."`
	Name string `json:"name,omitempty" jsonschema:"This node's name in the space; the pairing name when empty"`
}

// SpaceJoinResult is space_join's answer: the space, pinned and pending the owner's approval.
type SpaceJoinResult struct {
	Space protocol.SpacePin `json:"space"`
}

// SpaceMembersArgs are space_members' arguments.
type SpaceMembersArgs struct {
	Space string `json:"space,omitempty" jsonschema:"The space ID; may be left out when this node is in exactly one space"`
}

// SpaceMembersResult is space_members' answer: the members the owner vouches for now.
type SpaceMembersResult struct {
	Members []protocol.SpaceMember `json:"members"`
}

const (
	spaceJoinDescription = "Join a Pairbook space with the invite link its owner shared. The link's owner fingerprint is pinned here and never sent; " +
		"the request waits until the owner approves it. Joining shares nothing and creates no contact."
	spaceMembersDescription = "List a Pairbook space's members, only those the pinned owner vouches for now, to pick one to connect to."
)

// spaceJoin is the same operator_space join the CLI sends; the answer
// carries the pin, never the link or its token.
func (n *Node) spaceJoin(ctx context.Context, args SpaceJoinArgs) (protocol.SpacePin, error) {
	out, err := n.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpJoin, Link: args.Link, Name: args.Name})
	if err != nil {
		return protocol.SpacePin{}, err
	}
	if len(out.Pins) != 1 {
		return protocol.SpacePin{}, errors.New("join answered without its space")
	}
	return out.Pins[0], nil
}

func (n *Node) spaceMembers(ctx context.Context, args SpaceMembersArgs) ([]protocol.SpaceMember, error) {
	out, err := n.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMembers, Space: args.Space})
	if err != nil {
		return nil, err
	}
	return out.Members, nil
}

// space makes one space request and returns its result.
func (n *Node) space(ctx context.Context, r protocol.SpaceRequest) (*protocol.SpaceResult, error) {
	out, err := n.ask(ctx, protocol.Message{Type: protocol.TypeOperatorSpace, SpaceRequest: &r}, protocol.TypeOperatorSpaceResult)
	if err != nil {
		return nil, err
	}
	if out.SpaceResult == nil {
		return &protocol.SpaceResult{}, nil
	}
	return out.SpaceResult, nil
}

// SpaceConnectArgs are space_connect's arguments.
type SpaceConnectArgs struct {
	Member string `json:"member" jsonschema:"The member's key ID, as space_members lists it"`
	Space  string `json:"space,omitempty" jsonschema:"The space ID; may be left out when this node is in exactly one space"`
}

// SpaceConnectResult is space_connect's answer: the contact, new or already held.
type SpaceConnectResult struct {
	Contact protocol.PairingContactView `json:"contact"`
}

const spaceConnectDescription = "Connect to a member of a Pairbook space: both sides check each other is a member the owner vouches for, " +
	"then each holds the other as a contact, as after pairing. Nothing is shared: sharing agents stays a separate step."

func (n *Node) spaceConnect(ctx context.Context, args SpaceConnectArgs) (protocol.PairingContactView, error) {
	return protocol.PairingContactView{}, errors.New("space connect not implemented")
}
