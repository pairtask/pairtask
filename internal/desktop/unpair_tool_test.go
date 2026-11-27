package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestUnpairToolEndsOnlyConfirmedPairingAndKeepsHistory(t *testing.T) {
	fixture := newIncrementalFixture(t)
	fixture.incoming(t, 1)
	client := desktopSpaceMCP(t, fixture.node)
	ctx := context.Background()
	before, err := fixture.node.pairing(ctx)
	incrementalOK(t, err)
	args := UnpairArgs{Operation: uuid.NewString(), Contact: fixture.contact, Confirm: fixture.contact, Epoch: before.Contacts[0].PairingEpoch + 1}
	wrong, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "pair_unpair", Arguments: args})
	incrementalOK(t, err)
	desktopSpaceCheck(t, wrong.IsError, "ended a different pairing epoch")
	args.Epoch = before.Contacts[0].PairingEpoch
	args.Operation = uuid.NewString()
	desktopSpaceTool(t, client, "pair_unpair", args)
	desktopSpaceTool(t, client, "pair_unpair", args)
	after, err := fixture.node.pairing(ctx)
	incrementalOK(t, err)
	desktopSpaceCheck(t, after.Contacts[0].UnpairedAt != nil, "pair remained active")
	result := desktopSpaceTool(t, client, "check_inbox", map[string]any{"from": fixture.contact})
	record, err := json.Marshal(result.StructuredContent)
	incrementalOK(t, err)
	desktopSpaceCheck(t, bytes.Contains(record, []byte(`"in-1"`)), "saved message missing from history")
}
