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

var errSpacesNotImplemented = errors.New("spaces not implemented")

func (n *Node) spaceJoin(ctx context.Context, args SpaceJoinArgs) (protocol.SpacePin, error) {
	return protocol.SpacePin{}, errSpacesNotImplemented
}

func (n *Node) spaceMembers(ctx context.Context, args SpaceMembersArgs) ([]protocol.SpaceMember, error) {
	return nil, errSpacesNotImplemented
}
