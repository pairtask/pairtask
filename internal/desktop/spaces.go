package desktop

import (
	"context"
	"errors"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
)

// errCompanyLink: space_join was given a company Pairbook join link, which
// pairbook_join takes.
var errCompanyLink = errors.New("this is a company Pairbook join link (https://HOST/join#space=…&owner=…): join it with pairbook_join, not space_join")

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
	spaceJoinDescription = "Join a Pairbook space with the invite link its owner shared (https://HOST/v0/spaces/ID#token=...&owner=...); for a company Pairbook's join link (https://HOST/join#space=...) use pairbook_join instead. The link's owner fingerprint is pinned here and never sent; " +
		"the request waits until the owner approves it. Joining shares nothing and creates no contact."
	spaceMembersDescription = "List a Pairbook space's members, only those the pinned owner vouches for now, to pick one to connect to."
)

// spaceJoin is the same operator_space join the CLI sends; the answer
// carries the pin, never the link or its token.
func (n *Node) spaceJoin(ctx context.Context, args SpaceJoinArgs) (protocol.SpacePin, error) {
	if _, err := space.ParseOrgLink(args.Link); err == nil {
		return protocol.SpacePin{}, errCompanyLink
	}
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
