package hub

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestWorkspaceLocalNamesViewAcceptanceKeepsSelfNameAndCanonicalKey(t *testing.T) {
	h, key := remoteHubFixture(t)
	a := remoteHubClient(t, h, "sender", 13)
	remoteHubShare(t, h, key, a.ID)
	for _, alias := range []string{"Local workstation", "Renamed workstation", ""} {
		workspaceRoutingAlias(t, h, key, alias)
		out := h.workspaces(context.Background(), Inbound{From: a, Msg: protocol.Message{Type: protocol.TypeWorkspacesGet, RequestID: "local-names"}})
		messageSyncEqual(t, out.Type, protocol.TypeWorkspacesResult)
		messageSyncEqual(t, len(out.Workspaces), 1)
		view := out.Workspaces[0]
		messageSyncEqual(t, view.Key, key)
		messageSyncEqual(t, view.Name, "Remote workspace")
		encoded, err := json.Marshal(view)
		messageWorkerOK(t, err)
		var fields map[string]json.RawMessage
		messageWorkerOK(t, json.Unmarshal(encoded, &fields))
		localName, held := fields["local_name"]
		messageSyncEqual(t, held, alias != "")
		if held {
			var got string
			messageWorkerOK(t, json.Unmarshal(localName, &got))
			messageSyncEqual(t, got, alias)
		}
	}
}
