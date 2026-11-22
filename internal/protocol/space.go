package protocol

import (
	"encoding/json"
	"time"
)

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

	// Pairbook v1 (space maps). The person key never reaches the broker:
	// the host signs maps, invites and changes, and the broker checks,
	// keeps, sends and fetches them.
	SpaceOpMapAccept      = "map_accept"       // lineage, epoch, person, at → acceptance, workspace_key
	SpaceOpMapPublish     = "map_publish"      // directory, record (MapPut) → mark
	SpaceOpMapCurrent     = "map_current"      // space → record (SignedSpaceMap), mark
	SpaceOpMapChanges     = "map_changes"      // space → changes
	SpaceOpMapDrop        = "map_drop"         // space, digest
	SpaceOpMapInvite      = "map_invite"       // space, record (SignedMapInvite)
	SpaceOpMapInviteFetch = "map_invite_fetch" // link → record (SignedMapInvite)
	SpaceOpMapSubmit      = "map_submit"       // space or link, record (SignedChange), token for a join
	SpaceOpMapRefresh     = "map_refresh"      // space → mark
	SpaceOpMapStatus      = "map_status"       // → maps
	SpaceOpMapAddress     = "map_address"      // space, addresses, valid_for_ms (≤ 24h): publish this workspace's address, kept and renewed
	SpaceOpMapEnroll      = "map_enroll"       // link (an organization join link), name → enroll: pins the space, starts enrolling this workspace
	SpaceOpMapEnrollSend  = "map_enroll_send"  // space, enrollment, record (the person's join) → enroll_state
	SpaceOpMapEnrollState = "map_enroll_state" // space, enrollment → enroll_state
	SpaceOpMapShare       = "map_share"        // space, agent: list the agent in the space (advertises it; messaging stays with contact sharing)
	SpaceOpMapUnshare     = "map_unshare"      // space, agent: stop listing it
	SpaceOpMapPeople      = "map_people"       // space → people: who this workspace may see, online and listed agents; status_known false if the Pairbook keeps none

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

	// v1 fields. Record is the pairbook record the op names, as JSON.
	Record  json.RawMessage `json:"record,omitempty"`
	Digest  string          `json:"digest,omitempty"`  // map_drop: 64 hex
	Token   string          `json:"token,omitempty"`   // map_submit (join): the invite secret; never logged or echoed
	Lineage string          `json:"lineage,omitempty"` // map_accept: 64 hex
	Epoch   uint64          `json:"epoch,omitempty"`   // map_accept
	Person  string          `json:"person,omitempty"`  // map_accept: person key ID
	At      time.Time       `json:"at,omitempty"`      // map_accept
	Until   time.Time       `json:"until,omitempty"`   // map_submit (join): the invite's expiry; the join isn't retried after it

	Enrollment string `json:"enrollment,omitempty"` // map_enroll_send, map_enroll_state
	Agent      string `json:"agent,omitempty"`      // map_share, map_unshare
}

// SpaceResult answers a space operation.
type SpaceResult struct {
	Pins     []SpacePin    `json:"pins,omitempty"`     // list, create, join
	Invite   string        `json:"invite,omitempty"`   // invite: the link, a secret shown to the operator only
	Members  []SpaceMember `json:"members,omitempty"`  // members: verified against the pin
	Requests []SpaceMember `json:"requests,omitempty"` // requests: verified pending requests

	// v1 results.
	Record       json.RawMessage   `json:"record,omitempty"`        // map_current, map_invite_fetch
	Changes      []json.RawMessage `json:"changes,omitempty"`       // map_changes
	Invites      []string          `json:"invites,omitempty"`       // map_changes: per change, a join's invite state by this owner's ledger, else ""
	Mark         *SpaceMark        `json:"mark,omitempty"`          // map_publish, map_current, map_refresh
	Acceptance   []byte            `json:"acceptance,omitempty"`    // map_accept
	WorkspaceKey []byte            `json:"workspace_key,omitempty"` // map_accept
	Maps         []SpaceMapStatus  `json:"maps,omitempty"`          // map_status
	Stale        bool              `json:"stale,omitempty"`         // map_publish: another map won; fetch and fold again

	Enroll      *SpaceEnroll      `json:"enroll,omitempty"`       // map_enroll
	EnrollState *SpaceEnrollState `json:"enroll_state,omitempty"` // map_enroll_send, map_enroll_state
	People      []SpacePerson     `json:"people,omitempty"`       // map_people
	StatusKnown bool              `json:"status_known,omitempty"` // map_people: the Pairbook reported status
	Shares      []string          `json:"shares,omitempty"`       // map_share, map_unshare: the agents now listed
}

// SpacePerson is a person and the status of their workspaces this
// workspace may see.
type SpacePerson struct {
	Person     string           `json:"person"`
	Name       string           `json:"name"`
	Role       string           `json:"role"`
	Workspaces []SpaceWorkspace `json:"workspaces"`
}

// SpaceWorkspace is one workspace's status and the agents it lists.
type SpaceWorkspace struct {
	Workspace string       `json:"workspace"`
	Online    bool         `json:"online"`
	LastSeen  time.Time    `json:"last_seen,omitzero"`
	Agents    []SpaceAgent `json:"agents,omitempty"`
}

// SpaceAgent is an agent a workspace lists in a space.
type SpaceAgent struct {
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
}

// SpaceEnroll is a started enrollment: what the host signs the join with
// (the space, the map to base it on, the challenge whose SHA-256 is its
// token hash) and where the person confirms it.
type SpaceEnroll struct {
	Space      string    `json:"space"`
	Enrollment string    `json:"enrollment"`
	Challenge  []byte    `json:"challenge"`
	Base       SpaceMark `json:"base"`
	Code       string    `json:"code"`
	URL        string    `json:"url"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// SpaceEnrollState is an enrollment's state; Base, when stale, is the map
// the join is signed on again.
type SpaceEnrollState struct {
	State string     `json:"state"`
	Base  *SpaceMark `json:"base,omitempty"`
}

// Invite states map_changes gives a join: issued here and live; expired or
// given to another change; or not issued here (the host leaves the join
// pending rather than rejecting it).
const (
	InviteLive    = "live"
	InviteSpent   = "spent"
	InviteUnknown = "unknown"
)

// SpaceMark is a map's place: its version and digest (64 hex).
type SpaceMark struct {
	Version uint64 `json:"version"`
	Digest  string `json:"digest"`
}

// SpaceMapStatus is how one pinned space stands under its map.
type SpaceMapStatus struct {
	Space     string     `json:"space"`
	State     string     `json:"state"` // pending (no map with us yet) | active | expired | refused
	Mark      *SpaceMark `json:"mark,omitempty"`
	ExpiresAt time.Time  `json:"expires_at,omitempty"`
	Refresh   string     `json:"refresh_error,omitempty"`
	Managed   []string   `json:"managed,omitempty"` // contacts this space connected
	Refused   []string   `json:"refused,omitempty"` // workspaces the operator refused
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
