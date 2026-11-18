package protocol

import "time"

// Pairbook spaces (operator → broker): create or join a space in a Pairbook
// directory, approve members, publish this workspace's address. The broker
// signs with the workspace key and keeps a pin per space.
const (
	TypeOperatorSpace       = "operator_space"        // operator → broker, operator-only
	TypeOperatorSpaceResult = "operator_space_result" // broker → operator

	SpaceOpList     = "list"
	SpaceOpCreate   = "create"   // directory, optional id and name
	SpaceOpInvite   = "invite"   // space, optional valid_for_ms (default 24h)
	SpaceOpJoin     = "join"     // link, optional name
	SpaceOpRequests = "requests" // space
	SpaceOpApprove  = "approve"  // space, node; optional valid_for_ms (default 90 days)
	SpaceOpMembers  = "members"  // space
	SpaceOpPublish  = "publish"  // space, addresses; optional valid_for_ms (default 24h)
	SpaceOpRevoke   = "revoke"   // space, node
	SpaceOpLeave    = "leave"    // space
	SpaceOpDelete   = "delete"   // space

	// ErrSpace is a refused space operation; the detail names why.
	ErrSpace = "space_refused"

	// CapabilitySpaces: the broker serves operator_space.
	CapabilitySpaces = "spaces"
)

// SpaceRequest is one space operation. Space may be empty when exactly one
// space is pinned; otherwise the broker refuses and names the pins.
type SpaceRequest struct {
	Op        string   `json:"op"`
	Space     string   `json:"space,omitempty"`
	Directory string   `json:"directory,omitempty"`    // create: https://HOST[:PORT]
	ID        string   `json:"id,omitempty"`           // create: explicit space ID; random when empty
	Name      string   `json:"name,omitempty"`         // create, join: this workspace's name in the space; the pairing name when empty
	Link      string   `json:"link,omitempty"`         // join: the invite link
	Node      string   `json:"node,omitempty"`         // approve, revoke: the member's key ID
	Addresses []string `json:"addresses,omitempty"`    // publish: HOST:PORT
	ValidFor  int64    `json:"valid_for_ms,omitempty"` // invite, approve, publish
}

// SpaceResult answers a space operation.
type SpaceResult struct {
	Pins     []SpacePin    `json:"pins,omitempty"`     // list, create, join
	Invite   string        `json:"invite,omitempty"`   // invite: the link, a secret shown to the operator only
	Members  []SpaceMember `json:"members,omitempty"`  // members: verified against the pin
	Requests []SpaceMember `json:"requests,omitempty"` // requests: verified pending requests
}

// SpacePin is a space this workspace created or joined.
type SpacePin struct {
	Space     string    `json:"space"`
	Owner     string    `json:"owner"`     // the owner key ID this workspace trusts
	Directory string    `json:"directory"` // https://HOST[:PORT]
	JoinedAt  time.Time `json:"joined_at"`
	Owned     bool      `json:"owned,omitempty"` // this workspace is the owner
}

// SpaceMember is a member or a pending request.
type SpaceMember struct {
	Node string    `json:"node"` // key ID
	Name string    `json:"name"`
	Role string    `json:"role,omitempty"` // members only
	At   time.Time `json:"at"`             // a member's expiry, a request's time
}
