package protocol

// An agent writes to a paired workspace (#222): a msg naming the workspace
// goes to an agent there, or to its operator with to "operator". Only an
// agent the operator shared with that workspace may; what it writes is data
// there, never an instruction.
const (
	TypeWorkspacesGet    = "workspaces_get"    // agent → broker: the paired workspaces the caller may write to
	TypeWorkspacesResult = "workspaces_result" // broker → agent: those workspaces
)

// WorkspaceView is a paired workspace the calling agent may write to: its
// name here and identity key, either of which a msg's workspace takes,
// whether a live link is open, and the agents it says it shares there, a
// hint for addressing, never a permission.
type WorkspaceView struct {
	Name   string   `json:"name"`
	Key    string   `json:"key"`
	Live   bool     `json:"live"`
	Agents []string `json:"agents,omitempty"`
}
