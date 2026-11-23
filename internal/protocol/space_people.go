package protocol

// An agent reads who is in its workspace's Pairbook spaces (#421): the
// people its workspace may see, whether they are online, and the agents
// they list. It is read-only; listing advertises an agent, and who may
// message it is still the contact sharing.
const (
	SpacePeopleVersion    = 33
	TypeSpacePeopleGet    = "space_people_get"    // agent → broker, space_request.space: one space, or the only one pinned
	TypeSpacePeopleResult = "space_people_result" // broker → agent: space_result.people and status_known
)
