package bridge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/protocol"
)

type pairedClaimsClient struct {
	messagingClient
	requests []protocol.Message
}

func (c *pairedClaimsClient) Claim(_ context.Context, request protocol.Message) (ClaimsResult, error) {
	c.requests = append(c.requests, request)
	var out ClaimsResult
	err := json.Unmarshal([]byte(`{"message_id":"paired-subject","claims":[{"agent_id":"worker","task":"investigate","role":"owner","state":"active","remote":{"workspace":"sha256:peer"}}],"claims_total":1,"remote_visible":true}`), &out)
	return out, err
}

func TestPairedClaimsMCPAcceptanceMessageSelectorAndRemoteAttribution(t *testing.T) {
	for name, kind := range map[string]string{"claim": protocol.TypeClaimSet, "release_claim": protocol.TypeClaimRelease, "finish_claim": protocol.TypeClaimDone, "read_claims": protocol.TypeClaimGet} {
		t.Run(name, func(t *testing.T) {
			client := &pairedClaimsClient{}
			session := connectMCP(t, client)
			args := map[string]any{"message_id": "paired-subject"}
			if name != "read_claims" {
				args["task"] = "investigate"
			}
			result := call(t, session, name, args)
			assertEqual(t, "one broker operation", len(client.requests), 1)
			request := client.requests[0]
			assertEqual(t, "claim operation", request.Type, kind)
			assertEqual(t, "paired subject forwarded", request.MessageID, "paired-subject")
			assertEqual(t, "no operator authority invented", request.OperatorMessageID, "")
			data, err := json.Marshal(result.StructuredContent)
			requireNoError(t, err)
			var out struct {
				MessageID string `json:"message_id"`
				Claims    []struct {
					Remote *struct {
						Workspace string `json:"workspace"`
					} `json:"remote"`
				} `json:"claims"`
			}
			requireNoError(t, json.Unmarshal(data, &out))
			assertEqual(t, "result subject survives", out.MessageID, "paired-subject")
			assertEqual(t, "one claim survives", len(out.Claims), 1)
			if out.Claims[0].Remote == nil {
				t.Fatal("MCP stripped remote provenance")
			}
			assertEqual(t, "workspace attribution survives", out.Claims[0].Remote.Workspace, "sha256:peer")
		})
	}
}

func TestPairedClaimsMCPAcceptanceInvalidSelectorsNeverReachBroker(t *testing.T) {
	for _, name := range []string{"claim", "release_claim", "finish_claim", "read_claims"} {
		t.Run(name, func(t *testing.T) {
			client := &pairedClaimsClient{}
			session := connectMCP(t, client)
			for _, args := range []map[string]any{{"task": "investigate"}, {"task": "investigate", "message_id": "paired-subject", "operator_message_id": "operator-subject"}} {
				if name == "read_claims" {
					delete(args, "task")
				}
				result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
				requireNoError(t, err)
				if !result.IsError {
					t.Fatal("ambiguous or absent subject accepted")
				}
				assertEqual(t, "invalid input made no broker call", len(client.requests), 0)
			}
		})
	}
}
